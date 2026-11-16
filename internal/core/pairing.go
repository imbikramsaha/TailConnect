package core

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/hostops"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/sshconf"
	"github.com/imbikramsaha/tailconnect/internal/sshkeys"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

// outgoing pairing states.
const (
	outRequesting = "requesting"
	outWaiting    = "waiting" // the other device must approve
	outDenied     = "denied"
	outError      = "error"
	outDone       = "paired"
)

// outgoing is a pairing request we made.
type outgoing struct {
	Status  string
	Message string
	Code    string
	Auto    bool
	Updated time.Time
}

// pendingTTL is how long an outgoing request is polled; incoming requests
// are kept (persisted) for incomingTTL.
const (
	pendingTTL  = 10 * time.Minute
	incomingTTL = 7 * 24 * time.Hour
)

// PairCode derives the confirmation code both devices display for a
// request from requester (node + key) to target.
func PairCode(requesterNode, requesterKey, targetNode string) string {
	h := sha256.Sum256([]byte(requesterNode + "|" + requesterKey + "|" + targetNode))
	n := binary.BigEndian.Uint32(h[:4]) % 1000000
	return fmt.Sprintf("%03d %03d", n/1000, n%1000)
}

// ---- agent.Identifier / agent.Host implementation ----

// Identify resolves a tailnet caller via WhoIs.
func (c *Core) Identify(ctx context.Context, remoteAddr string) (agent.Caller, error) {
	w, err := c.node.WhoIs(ctx, remoteAddr)
	if err != nil {
		return agent.Caller{}, err
	}
	if w.Node == nil {
		return agent.Caller{}, errors.New("unknown node")
	}
	// Never accept this device itself as a caller. With the Tailscale app
	// in TUN mode, any local process (including other OS users) can connect
	// to this machine's own Tailscale IP; WhoIs then reports our own node —
	// same Tailscale user — which would otherwise be auto-trusted.
	self, err := c.selfNode(ctx)
	if err != nil {
		return agent.Caller{}, err
	}
	if string(w.Node.StableID) == self {
		return agent.Caller{}, errors.New("connections from this device itself are not accepted")
	}
	cl := agent.Caller{
		NodeID: string(w.Node.StableID),
		Name:   w.Node.ComputedName,
		Tagged: w.Node.IsTagged(),
		Addr:   remoteAddr,
	}
	if cl.Name == "" {
		cl.Name = w.Node.Name
	}
	if w.UserProfile != nil {
		cl.LoginName = w.UserProfile.LoginName
		cl.UserID = int64(w.UserProfile.ID)
	}
	return cl, nil
}

// selfNode returns this device's tailnet node ID (fetched if not yet known).
func (c *Core) selfNode(ctx context.Context) (string, error) {
	c.mu.Lock()
	id := c.selfNodeID
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	st, err := c.node.Status(ctx)
	if err != nil || st.Self == nil || st.Self.ID == "" {
		return "", errors.New("tailnet identity not known yet")
	}
	c.mu.Lock()
	c.selfNodeID = string(st.Self.ID)
	c.mu.Unlock()
	return string(st.Self.ID), nil
}

// Hello describes this device to caller cl with the given access level.
func (c *Core) Hello(_ agent.Caller, access string) agent.Hello {
	st := c.store.Get().Settings
	port := st.LocalSSHPort
	h := agent.Hello{
		App:       agent.AppID,
		Version:   c.opts.Version,
		Protocol:  agent.ProtocolVersion,
		Name:      c.selfName,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		SSH:       netx.PortOpen(port),
		SSHPort:   port,
		Trusted:   access != "",
		Access:    access,
		DeviceKey: c.id.PublicLine,
	}
	// Reveal only what the caller's access level needs.
	if store.AccessRank(access) >= store.AccessRank(store.AccessPorts) && access != "" {
		h.MACs, h.LANs = c.lanInfo()
	}
	if access == store.AccessFull {
		h.User, h.Home = c.osUser, c.home
		if c.builtinSSHRunning() {
			h.BuiltinSSH = agent.SSHPort
			h.HostKey = c.hostKeyLine()
		}
	}
	return h
}

var (
	lanMu   sync.Mutex
	lanMACs []string
	lanNets []string
	lanAt   time.Time
)

// lanInfo caches hostops.LANInfo (it shells out on macOS).
func (c *Core) lanInfo() ([]string, []string) {
	lanMu.Lock()
	defer lanMu.Unlock()
	if time.Since(lanAt) > time.Minute {
		lanMACs, lanNets = hostops.LANInfo()
		lanAt = time.Now()
	}
	return slices.Clone(lanMACs), slices.Clone(lanNets)
}

// Access returns the access level nodeID has on this device ("" = none).
func (c *Core) Access(nodeID string) string {
	st := c.store.Get()
	t, ok := st.Trust(nodeID)
	if !ok {
		return ""
	}
	return t.Level()
}

// IsTrusted reports whether nodeID is paired with (trusted by) this device.
func (c *Core) IsTrusted(nodeID string) bool { return c.Access(nodeID) != "" }

// AllowPort reports whether nodeID may tunnel to local port.
func (c *Core) AllowPort(nodeID string, port int) bool {
	st := c.store.Get()
	t, ok := st.Trust(nodeID)
	if !ok || store.AccessRank(t.Level()) < store.AccessRank(store.AccessPorts) {
		return false
	}
	if port == st.Settings.LocalSSHPort && t.Level() != store.AccessFull {
		return false // the SSH server is a shell; only full access
	}
	return len(t.Ports) == 0 || slices.Contains(t.Ports, port)
}

// Event records an access event from a peer and notifies when useful.
func (c *Core) Event(cl agent.Caller, kind, detail string) {
	name := c.trustedName(cl)
	c.logs.Session.Printf("%q: %s %q", name, kind, detail)
	switch kind {
	case "file":
		c.notify("File received from "+name, filepath.Base(detail)+" was saved to Downloads/TailConnect", "")
		go func() { _ = launch.Reveal(detail) }()
	case "clipboard":
		c.notify("Clipboard received", name+" sent you its clipboard", "")
	}
}

func (c *Core) trustedName(cl agent.Caller) string {
	st := c.store.Get()
	if t, ok := st.Trust(cl.NodeID); ok && t.Name != "" {
		return t.Name
	}
	return cl.Name
}

func (c *Core) approvedResponse(cl agent.Caller, code string) agent.PairResponse {
	h := c.Hello(cl, c.Access(cl.NodeID))
	return agent.PairResponse{Status: agent.PairApproved, User: h.User, Home: h.Home, SSH: h.SSH, SSHPort: h.SSHPort, Code: code}
}

// HandlePair processes a pairing request from another device.
func (c *Core) HandlePair(_ context.Context, cl agent.Caller, req agent.PairRequest) agent.PairResponse {
	pub, err := sshkeys.ValidatePublicKey(req.PublicKey)
	if err != nil {
		return agent.PairResponse{Status: agent.PairDenied, Message: err.Error()}
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") { // TailConnect device keys are always ed25519
		return agent.PairResponse{Status: agent.PairDenied, Message: "only ed25519 device keys are accepted"}
	}
	req.Name = SafeName(req.Name)
	if req.Name == "" {
		req.Name = SafeName(cl.Name)
	}
	req.OS = SafeOS(req.OS)
	st := c.store.Get()
	c.mu.Lock()
	selfUser, selfNode := c.selfUserID, c.selfNodeID
	c.mu.Unlock()
	// The confirmation code includes a fresh random value from this device,
	// so a requester can't search for a key whose code matches someone
	// else's request.
	code := PairCode(cl.NodeID, pub, selfNode+"|"+randomHex(16))
	auto := st.Settings.AutoTrustOwnDevices && !cl.Tagged && cl.UserID != 0 && cl.UserID == selfUser &&
		!slices.Contains(st.Ignored, cl.NodeID)
	t, trusted := st.Trust(cl.NodeID)
	switch {
	case trusted && t.PublicKey == pub:
		// Re-pairing with the same key: nothing changes.
		return c.approvedResponse(cl, "")
	case trusted:
		// A different key for a paired device — even one on your account.
		// Every program and OS user on that machine shares its Tailscale
		// identity, so this could be one of them trying to take over the
		// device's access. The paired key stays trusted; ask about the new one.
		c.logs.Session.Printf("%q presented a new key; approval required", req.Name)
	case auto:
		level := st.Settings.OwnDeviceAccess
		if err := c.trust(cl, req.Name, pub, level, nil, true); err != nil {
			return agent.PairResponse{Status: agent.PairDenied, Message: err.Error()}
		}
		c.logs.Session.Printf("auto-trusted own device %q (%s) with %s access", req.Name, cl.LoginName, level)
		c.notify(req.Name+" can now reach this computer", "Paired automatically because it's on your Tailscale account ("+accessWord(level)+"). Review in Settings → Devices & access.", "#/settings/access")
		return c.approvedResponse(cl, "")
	}
	// Recently declined: don't re-ask (and re-notify) for an hour.
	for _, p := range st.Pending {
		if p.NodeID == cl.NodeID && p.Status == agent.PairDenied && time.Since(p.Created) < time.Hour {
			return agent.PairResponse{Status: agent.PairDenied, Message: "the other device declined"}
		}
	}

	var id string
	isNew := false
	err = c.store.Update(func(s *store.State) error {
		open := 0
		for i := 0; i < len(s.Pending); i++ {
			pp := &s.Pending[i]
			if pp.Status != agent.PairPending {
				continue
			}
			if pp.NodeID != cl.NodeID {
				open++
				continue
			}
			if pp.PublicKey == pub {
				// The same request again (the requester polls by re-sending).
				id, code = pp.ID, pp.Code
				if pp.Name == req.Name && pp.OS == req.OS {
					return errUnchanged
				}
				pp.Name, pp.OS = req.Name, req.OS
				return nil
			}
			// A different key from the same device: withdraw the old request
			// rather than swap the key under it, so an approval can only ever
			// apply to the key (and code) the user was shown.
			s.Pending = slices.Delete(s.Pending, i, i+1)
			i--
		}
		if open >= maxPendingRequests {
			return errTooManyRequests
		}
		id, isNew = newID("pr-"), true
		s.Pending = append(s.Pending, store.Pending{
			ID: id, NodeID: cl.NodeID, NodeName: cl.Name, LoginName: cl.LoginName, UserID: cl.UserID,
			Name: req.Name, OS: req.OS, PublicKey: pub, Code: code, Status: agent.PairPending, Created: time.Now(),
		})
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		return agent.PairResponse{Status: agent.PairDenied, Message: err.Error()}
	}
	if isNew {
		c.logs.Session.Printf("pairing request from %q (%s) awaiting approval, code %s", req.Name, cl.LoginName, code)
		c.notify("Pairing request from "+req.Name, "Confirm code "+code+" matches on "+req.Name+", then approve.", "#/")
	}
	go c.events.Notify()
	return agent.PairResponse{Status: agent.PairPending, ID: id, Code: code}
}

func (c *Core) pending(id string) (store.Pending, bool) {
	st := c.store.Get()
	for _, p := range st.Pending {
		if p.ID == id {
			return p, true
		}
	}
	return store.Pending{}, false
}

// PairStatus answers a requester polling its pending request.
func (c *Core) PairStatus(cl agent.Caller, id string) agent.PairResponse {
	p, ok := c.pending(id)
	status := ""
	if ok && p.NodeID == cl.NodeID {
		status = p.Status
	}
	switch {
	case status == agent.PairApproved || (status == "" && c.IsTrusted(cl.NodeID)):
		return c.approvedResponse(cl, p.Code)
	case status == agent.PairPending:
		return agent.PairResponse{Status: agent.PairPending, ID: id, Code: p.Code}
	case status == agent.PairDenied:
		return agent.PairResponse{Status: agent.PairDenied, Message: "the other device declined"}
	}
	return agent.PairResponse{Status: agent.PairDenied, Message: "request expired"}
}

// Unpair handles a peer telling us to forget it. If its access here was
// restricted (lower than new own devices get, or limited to some ports),
// it must be approved to come back — otherwise unpairing and auto-pairing
// again would lift the restriction.
func (c *Core) Unpair(cl agent.Caller) error {
	c.logs.Session.Printf("device %q unpaired itself", cl.Name)
	st := c.store.Get()
	if t, ok := st.Trust(cl.NodeID); ok && (len(t.Ports) > 0 || store.AccessRank(t.Level()) < store.AccessRank(st.Settings.OwnDeviceAccess)) {
		_ = c.store.Update(func(st *store.State) error {
			if !slices.Contains(st.Ignored, cl.NodeID) {
				st.Ignored = append(st.Ignored, cl.NodeID)
			}
			return nil
		})
	}
	return c.untrust(cl.NodeID)
}

var (
	errUnchanged       = errors.New("unchanged")
	errTooManyRequests = errors.New("too many pairing requests are waiting; approve or decline some first")
	errRevoked         = errors.New("access to this device was revoked")
)

// maxPendingRequests caps requests awaiting a decision (each may notify).
const maxPendingRequests = 32

func accessWord(level string) string {
	switch level {
	case store.AccessStats:
		return "monitor only"
	case store.AccessPorts:
		return "ports & monitor"
	}
	return "full access"
}

// PeerKey returns the key nodeID paired with.
func (c *Core) PeerKey(nodeID string) string {
	st := c.store.Get()
	if t, ok := st.Trust(nodeID); ok {
		return t.PublicKey
	}
	return ""
}

// PendingKey returns the key of nodeID's pending request id.
func (c *Core) PendingKey(nodeID, id string) string {
	if p, ok := c.pending(id); ok && p.NodeID == nodeID {
		return p.PublicKey
	}
	return ""
}

// ---- trust management ----

// trust records cl's key with an access level. auto marks automatic
// (own-device) trust, which a revocation made meanwhile must win over.
func (c *Core) trust(cl agent.Caller, name, pub, level string, ports []int, auto bool) error {
	if name == "" {
		name = cl.Name
	}
	if !store.ValidAccess(level) {
		level = store.AccessFull
	}
	if auto && slices.Contains(c.store.Get().Ignored, cl.NodeID) {
		return errRevoked
	}
	if err := c.syncAuthorizedKey(cl.NodeID, name, pub, level); err != nil {
		return err
	}
	err := c.store.Update(func(st *store.State) error {
		if auto && slices.Contains(st.Ignored, cl.NodeID) {
			return errRevoked // revoked while the key was being installed
		}
		st.Trusted = slices.DeleteFunc(st.Trusted, func(t store.TrustedPeer) bool { return t.NodeID == cl.NodeID })
		st.Trusted = append(st.Trusted, store.TrustedPeer{
			NodeID: cl.NodeID, Name: name, LoginName: cl.LoginName, PublicKey: pub, TrustedAt: time.Now(),
			Access: level, Ports: ports,
		})
		return nil
	})
	if errors.Is(err, errRevoked) {
		_ = sshkeys.Revoke(c.opts.AuthorizedKeys, cl.NodeID)
	}
	// Sessions and tunnels opened with a previous key or level end now.
	c.dropSSHSessions(cl.NodeID)
	if c.agentSrv != nil {
		c.agentSrv.CloseNode(cl.NodeID)
	}
	c.events.Notify()
	return err
}

// syncAuthorizedKey installs the peer's key for the system SSH server only
// when it has full access.
func (c *Core) syncAuthorizedKey(nodeID, name, pub, level string) error {
	if level == store.AccessFull {
		if err := sshkeys.Authorize(c.opts.AuthorizedKeys, pub, nodeID, name); err != nil {
			return fmt.Errorf("installing SSH key: %w", err)
		}
		return nil
	}
	return sshkeys.Revoke(c.opts.AuthorizedKeys, nodeID)
}

func (c *Core) untrust(nodeID string) error {
	errKey := sshkeys.Revoke(c.opts.AuthorizedKeys, nodeID)
	err := c.store.Update(func(st *store.State) error {
		st.Trusted = slices.DeleteFunc(st.Trusted, func(t store.TrustedPeer) bool { return t.NodeID == nodeID })
		return nil
	})
	c.dropSSHSessions(nodeID)
	if c.agentSrv != nil {
		c.agentSrv.CloseNode(nodeID)
	}
	c.events.Notify()
	return errors.Join(errKey, err)
}

// SetAccess changes what a paired device may do on this one.
func (c *Core) SetAccess(nodeID, level string, ports []int) error {
	if !store.ValidAccess(level) {
		return errors.New("access must be full, ports or stats")
	}
	for _, p := range ports {
		if !netx.ValidPort(p) {
			return fmt.Errorf("invalid port %d", p)
		}
	}
	st := c.store.Get()
	t, ok := st.Trust(nodeID)
	if !ok {
		return errors.New("that device is not paired")
	}
	if err := c.syncAuthorizedKey(nodeID, t.Name, t.PublicKey, level); err != nil {
		return err
	}
	err := c.store.Update(func(st *store.State) error {
		for i := range st.Trusted {
			if st.Trusted[i].NodeID == nodeID {
				st.Trusted[i].Access, st.Trusted[i].Ports = level, slices.Clone(ports)
			}
		}
		return nil
	})
	if level != store.AccessFull {
		c.dropSSHSessions(nodeID)
	}
	if c.agentSrv != nil && (store.AccessRank(level) < store.AccessRank(t.Level()) || len(ports) > 0) {
		c.agentSrv.CloseNode(nodeID) // reopen with the new rules
	}
	c.logs.Session.Printf("access for %q set to %s%s", t.Name, level, portsNote(ports))
	c.events.Notify()
	return err
}

func portsNote(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	return fmt.Sprintf(" (ports %v)", ports)
}

// ApprovePair approves (or denies) an incoming request with an access level.
func (c *Core) ApprovePair(id string, approve bool, level string) error {
	p, ok := c.pending(id)
	if !ok || p.Status != agent.PairPending {
		return errors.New("no such pending request")
	}
	if approve && !store.ValidAccess(level) {
		return errors.New("access must be full, ports or stats")
	}
	setStatus := func(status string) error {
		return c.store.Update(func(st *store.State) error {
			for i := range st.Pending {
				if st.Pending[i].ID == id {
					st.Pending[i].Status = status
				}
			}
			return nil
		})
	}
	if !approve {
		c.logs.Session.Printf("declined pairing request from %q", p.Name)
		err := setStatus(agent.PairDenied)
		c.events.Notify()
		return err
	}
	cl := agent.Caller{NodeID: p.NodeID, Name: p.NodeName, LoginName: p.LoginName, UserID: p.UserID}
	_ = c.store.Update(func(st *store.State) error {
		st.Ignored = slices.DeleteFunc(st.Ignored, func(s string) bool { return s == p.NodeID })
		return nil
	})
	// A new key for a device already paired keeps its port limit.
	var ports []int
	cur := c.store.Get()
	if t, ok := cur.Trust(p.NodeID); ok {
		ports = t.Ports
	}
	if err := c.trust(cl, p.Name, p.PublicKey, level, ports, false); err != nil {
		return err
	}
	if err := setStatus(agent.PairApproved); err != nil {
		return err
	}
	c.logs.Session.Printf("approved pairing request from %q (%s) with %s access", p.Name, p.LoginName, level)
	// Offer the reverse direction too, so both sides can reach each other.
	c.mu.Lock()
	_, known := c.peers[p.NodeID]
	c.mu.Unlock()
	if known && c.store.Get().FindDeviceByNode(p.NodeID) < 0 {
		c.startPair(p.NodeID, false)
	}
	return nil
}

// RevokeTrust stops a device from accessing this one.
func (c *Core) RevokeTrust(nodeID string) error {
	c.logs.Session.Printf("revoked access for %s", nodeID)
	// Block auto-pairing too, or a device on the same account would simply
	// pair again. Pairing it again by hand (or approving a request) lifts it.
	_ = c.store.Update(func(st *store.State) error {
		if !slices.Contains(st.Ignored, nodeID) {
			st.Ignored = append(st.Ignored, nodeID)
		}
		return nil
	})
	return c.untrust(nodeID)
}

// ---- outgoing pairing ----

// PairDevice asks a discovered TailConnect device to pair (device ID "p-<node>").
func (c *Core) PairDevice(deviceID string) error {
	nodeID, ok := nodeFromDeviceID(deviceID)
	if !ok {
		return errors.New("only TailConnect devices on your tailnet can be paired")
	}
	p, ok := c.peer(nodeID)
	if !ok || p.Hello == nil {
		return errors.New("device is not running TailConnect or is offline")
	}
	_ = c.store.Update(func(st *store.State) error {
		st.Ignored = slices.DeleteFunc(st.Ignored, func(s string) bool { return s == nodeID })
		return nil
	})
	c.startPair(nodeID, false)
	return nil
}

func (c *Core) startPair(nodeID string, auto bool) {
	c.mu.Lock()
	if o := c.outgoing[nodeID]; o != nil && (o.Status == outRequesting || o.Status == outWaiting) {
		c.mu.Unlock()
		return
	}
	c.outgoing[nodeID] = &outgoing{Status: outRequesting, Auto: auto, Updated: time.Now()}
	c.mu.Unlock()
	c.events.Notify()
	go c.runPair(nodeID, auto)
}

func (c *Core) setOutgoing(nodeID, status, msg string) {
	c.mu.Lock()
	auto, code := false, ""
	if o := c.outgoing[nodeID]; o != nil {
		auto, code = o.Auto, o.Code
	}
	c.outgoing[nodeID] = &outgoing{Status: status, Message: msg, Code: code, Auto: auto, Updated: time.Now()}
	c.mu.Unlock()
	c.events.Notify()
}

func (c *Core) runPair(nodeID string, auto bool) {
	p, ok := c.peer(nodeID)
	if !ok {
		c.setOutgoing(nodeID, outError, "device went offline")
		return
	}
	req := agent.PairRequest{Name: c.selfName, OS: runtime.GOOS, PublicKey: c.id.PublicLine}
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	resp, err := c.agentCli.Pair(ctx, p.IP, req)
	cancel()
	if err != nil {
		c.setOutgoing(nodeID, outError, err.Error())
		return
	}
	c.mu.Lock()
	if o := c.outgoing[nodeID]; o != nil {
		o.Code = resp.Code
	}
	c.mu.Unlock()
	if auto && resp.Status == agent.PairPending {
		// The other device wants a manual approval; don't keep nagging it.
		c.setOutgoing(nodeID, outDenied, "Waiting for approval on "+displayName(p))
		return
	}
	deadline := time.Now().Add(pendingTTL)
	for resp.Status == agent.PairPending {
		c.setOutgoing(nodeID, outWaiting, "Approve the request on "+displayName(p)+" — check it shows code "+resp.Code)
		if time.Now().After(deadline) {
			c.setOutgoing(nodeID, outError, "pairing request timed out")
			return
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if pp, ok := c.peer(nodeID); ok {
			p = pp
		}
		ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
		r, err := c.agentCli.PairStatus(ctx, p.IP, resp.ID)
		cancel()
		if err != nil {
			continue // transient; keep polling until deadline
		}
		resp = r
	}
	if resp.Status != agent.PairApproved {
		c.setOutgoing(nodeID, outDenied, resp.Message)
		return
	}
	if err := c.savePeerDevice(p, resp); err != nil {
		c.setOutgoing(nodeID, outError, err.Error())
		return
	}
	// Learn the SSH server, home dir and wake info now that we're trusted.
	hctx, hcancel := context.WithTimeout(c.ctx, 8*time.Second)
	if h, err := c.verifiedHello(hctx, nodeID, p.IP); err == nil && h.Trusted {
		if d, ok := c.device("p-" + nodeID); ok {
			c.applyHello(d, h)
		}
		c.mu.Lock()
		if pp := c.peers[nodeID]; pp != nil {
			pp.Hello = h
		}
		c.mu.Unlock()
	}
	hcancel()
	c.setOutgoing(nodeID, outDone, "")
	how := "paired with"
	if auto {
		how = "auto-paired with own device"
	}
	c.logs.Session.Printf("%s %q", how, displayName(p))
	c.Kick()
}

func displayName(p peerInfo) string {
	if p.Hello != nil {
		if n := SafeName(p.Hello.Name); n != "" {
			return n
		}
	}
	if n := SafeName(p.HostName); n != "" {
		return n
	}
	return SafeName(shortName(p.DNSName))
}

var errRemoved = errors.New("device was removed")

func (c *Core) savePeerDevice(p peerInfo, resp *agent.PairResponse) error {
	user, _ := SafeUser(resp.User)
	home, _ := SafeHome(resp.Home)
	err := c.store.Update(func(st *store.State) error {
		// A pairing that was already in flight when the user removed the
		// device must not bring it back.
		if slices.Contains(st.Ignored, p.NodeID) {
			return errRemoved
		}
		port := resp.SSHPort
		if port <= 0 || port > 65535 {
			port = 22
		}
		if i := st.FindDeviceByNode(p.NodeID); i >= 0 {
			d := &st.Devices[i]
			d.User, d.Home, d.Port = user, home, port
			if p.Hello != nil {
				d.OS = SafeOS(p.Hello.OS)
			}
			return nil
		}
		name := displayName(p)
		d := store.Device{
			ID: "p-" + p.NodeID, Kind: store.KindPeer, Name: name, Alias: uniqueAlias(st, name, ""),
			NodeID: p.NodeID, User: user, Home: home, Port: port, AddedAt: time.Now(),
		}
		if p.Hello != nil {
			d.OS = SafeOS(p.Hello.OS)
		}
		st.Devices = append(st.Devices, d)
		return nil
	})
	if err != nil {
		return err
	}
	return c.writeSSHConfig()
}

func nodeFromDeviceID(id string) (string, bool) {
	if len(id) > 2 && id[:2] == "p-" {
		return id[2:], true
	}
	return "", false
}

// uniqueAlias returns an ssh alias for name not used by other devices.
func uniqueAlias(st *store.State, name, exceptID string) string {
	base := sshconf.Alias(name)
	alias := base
	for i := 2; ; i++ {
		taken := slices.ContainsFunc(st.Devices, func(d store.Device) bool { return d.Alias == alias && d.ID != exceptID })
		if !taken {
			return alias
		}
		alias = fmt.Sprintf("%s-%d", base, i)
	}
}

// expirePending drops old or decided incoming requests.
func (c *Core) expirePending() {
	st := c.store.Get()
	stale := slices.ContainsFunc(st.Pending, func(p store.Pending) bool {
		return time.Since(p.Created) > incomingTTL || (p.Status != agent.PairPending && time.Since(p.Created) > time.Hour)
	})
	if !stale {
		return
	}
	_ = c.store.Update(func(st *store.State) error {
		st.Pending = slices.DeleteFunc(st.Pending, func(p store.Pending) bool {
			return time.Since(p.Created) > incomingTTL || (p.Status != agent.PairPending && time.Since(p.Created) > time.Hour)
		})
		return nil
	})
}
