package core

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/sshconf"
	"github.com/imbikramsaha/tailconnect/internal/sshkeys"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

// incoming is a pairing request made to this device.
type incoming struct {
	ID      string
	Caller  agent.Caller
	Req     agent.PairRequest
	PubKey  string
	Status  string
	Created time.Time
}

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
	Auto    bool
	Updated time.Time
}

const pendingTTL = 10 * time.Minute

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

// Hello describes this device.
func (c *Core) Hello(trusted bool) agent.Hello {
	port := c.store.Get().Settings.LocalSSHPort
	h := agent.Hello{
		App:      agent.AppID,
		Version:  c.opts.Version,
		Protocol: agent.ProtocolVersion,
		Name:     c.selfName,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		SSH:      netx.PortOpen(port),
		SSHPort:  port,
		Trusted:  trusted,
	}
	if trusted {
		h.User, h.Home = c.osUser, c.home
	}
	return h
}

// IsTrusted reports whether nodeID is paired with (trusted by) this device.
func (c *Core) IsTrusted(nodeID string) bool {
	st := c.store.Get()
	return st.IsTrusted(nodeID)
}

func (c *Core) approvedResponse() agent.PairResponse {
	h := c.Hello(true)
	return agent.PairResponse{Status: agent.PairApproved, User: h.User, Home: h.Home, SSH: h.SSH, SSHPort: h.SSHPort}
}

// HandlePair processes a pairing request from another device.
func (c *Core) HandlePair(_ context.Context, cl agent.Caller, req agent.PairRequest) agent.PairResponse {
	pub, err := sshkeys.ValidatePublicKey(req.PublicKey)
	if err != nil {
		return agent.PairResponse{Status: agent.PairDenied, Message: err.Error()}
	}
	st := c.store.Get()
	c.mu.Lock()
	selfUser := c.selfUserID
	c.mu.Unlock()
	auto := st.Settings.AutoTrustOwnDevices && !cl.Tagged && cl.UserID != 0 && cl.UserID == selfUser &&
		!slices.Contains(st.Ignored, cl.NodeID)
	if st.IsTrusted(cl.NodeID) || auto {
		if err := c.trust(cl, req.Name, pub); err != nil {
			return agent.PairResponse{Status: agent.PairDenied, Message: err.Error()}
		}
		if auto {
			c.logs.Session.Printf("auto-trusted own device %q (%s)", req.Name, cl.LoginName)
		}
		return c.approvedResponse()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, in := range c.incoming {
		if in.Caller.NodeID == cl.NodeID && in.Status == agent.PairPending {
			in.Req, in.PubKey = req, pub
			return agent.PairResponse{Status: agent.PairPending, ID: in.ID}
		}
	}
	in := &incoming{ID: newID("pr-"), Caller: cl, Req: req, PubKey: pub, Status: agent.PairPending, Created: time.Now()}
	c.incoming[in.ID] = in
	c.logs.Session.Printf("pairing request from %q (%s) awaiting approval", req.Name, cl.LoginName)
	go c.events.Notify()
	return agent.PairResponse{Status: agent.PairPending, ID: in.ID}
}

// PairStatus answers a requester polling its pending request.
func (c *Core) PairStatus(cl agent.Caller, id string) agent.PairResponse {
	c.mu.Lock()
	in, ok := c.incoming[id]
	var status string
	if ok && in.Caller.NodeID == cl.NodeID {
		status = in.Status
	}
	c.mu.Unlock()
	switch {
	case status == agent.PairApproved || (status == "" && c.IsTrusted(cl.NodeID)):
		return c.approvedResponse()
	case status == agent.PairPending:
		return agent.PairResponse{Status: agent.PairPending, ID: id}
	case status == agent.PairDenied:
		return agent.PairResponse{Status: agent.PairDenied, Message: "the other device declined"}
	}
	return agent.PairResponse{Status: agent.PairDenied, Message: "request expired"}
}

// Unpair handles a peer telling us to forget it.
func (c *Core) Unpair(cl agent.Caller) error {
	c.logs.Session.Printf("device %q unpaired itself", cl.Name)
	return c.untrust(cl.NodeID)
}

// ---- trust management ----

func (c *Core) trust(cl agent.Caller, name, pub string) error {
	if name == "" {
		name = cl.Name
	}
	if err := sshkeys.Authorize(c.opts.AuthorizedKeys, pub, cl.NodeID, name); err != nil {
		return fmt.Errorf("installing SSH key: %w", err)
	}
	err := c.store.Update(func(st *store.State) error {
		st.Trusted = slices.DeleteFunc(st.Trusted, func(t store.TrustedPeer) bool { return t.NodeID == cl.NodeID })
		st.Trusted = append(st.Trusted, store.TrustedPeer{
			NodeID: cl.NodeID, Name: name, LoginName: cl.LoginName, PublicKey: pub, TrustedAt: time.Now(),
		})
		return nil
	})
	c.events.Notify()
	return err
}

func (c *Core) untrust(nodeID string) error {
	errKey := sshkeys.Revoke(c.opts.AuthorizedKeys, nodeID)
	err := c.store.Update(func(st *store.State) error {
		st.Trusted = slices.DeleteFunc(st.Trusted, func(t store.TrustedPeer) bool { return t.NodeID == nodeID })
		return nil
	})
	c.events.Notify()
	return errors.Join(errKey, err)
}

// ApprovePair approves (or denies) an incoming request.
func (c *Core) ApprovePair(id string, approve bool) error {
	c.mu.Lock()
	in, ok := c.incoming[id]
	if !ok || in.Status != agent.PairPending {
		c.mu.Unlock()
		return errors.New("no such pending request")
	}
	in.Status = agent.PairDenied
	if approve {
		in.Status = agent.PairApproved
	}
	cl, req, pub := in.Caller, in.Req, in.PubKey
	c.mu.Unlock()
	if !approve {
		c.logs.Session.Printf("declined pairing request from %q", req.Name)
		c.events.Notify()
		return nil
	}
	if err := c.trust(cl, req.Name, pub); err != nil {
		c.mu.Lock()
		in.Status = agent.PairPending
		c.mu.Unlock()
		return err
	}
	c.logs.Session.Printf("approved pairing request from %q (%s)", req.Name, cl.LoginName)
	// Offer the reverse direction too, so both sides can reach each other.
	c.mu.Lock()
	_, known := c.peers[cl.NodeID]
	c.mu.Unlock()
	if known && c.store.Get().FindDeviceByNode(cl.NodeID) < 0 {
		c.startPair(cl.NodeID, false)
	}
	return nil
}

// RevokeTrust stops a device from accessing this one.
func (c *Core) RevokeTrust(nodeID string) error {
	c.logs.Session.Printf("revoked access for %s", nodeID)
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
	auto := false
	if o := c.outgoing[nodeID]; o != nil {
		auto = o.Auto
	}
	c.outgoing[nodeID] = &outgoing{Status: status, Message: msg, Auto: auto, Updated: time.Now()}
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
	if auto && resp.Status == agent.PairPending {
		// The other device wants a manual approval; don't keep nagging it.
		c.setOutgoing(nodeID, outDenied, "Waiting for approval on "+displayName(p))
		return
	}
	deadline := time.Now().Add(pendingTTL)
	for resp.Status == agent.PairPending {
		c.setOutgoing(nodeID, outWaiting, "Approve the request on "+p.HostName)
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
	c.setOutgoing(nodeID, outDone, "")
	how := "paired with"
	if auto {
		how = "auto-paired with own device"
	}
	c.logs.Session.Printf("%s %q", how, displayName(p))
	c.Kick()
}

func displayName(p peerInfo) string {
	if p.Hello != nil && p.Hello.Name != "" {
		return p.Hello.Name
	}
	if p.HostName != "" {
		return p.HostName
	}
	return shortName(p.DNSName)
}

func (c *Core) savePeerDevice(p peerInfo, resp *agent.PairResponse) error {
	err := c.store.Update(func(st *store.State) error {
		port := resp.SSHPort
		if port == 0 {
			port = 22
		}
		if i := st.FindDeviceByNode(p.NodeID); i >= 0 {
			d := &st.Devices[i]
			d.User, d.Home, d.Port = resp.User, resp.Home, port
			if p.Hello != nil {
				d.OS = p.Hello.OS
			}
			return nil
		}
		name := displayName(p)
		d := store.Device{
			ID: "p-" + p.NodeID, Kind: store.KindPeer, Name: name, Alias: uniqueAlias(st, name, ""),
			NodeID: p.NodeID, User: resp.User, Home: resp.Home, Port: port, AddedAt: time.Now(),
		}
		if p.Hello != nil {
			d.OS = p.Hello.OS
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

// expirePending drops stale incoming requests.
func (c *Core) expirePending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, in := range c.incoming {
		if time.Since(in.Created) > pendingTTL {
			delete(c.incoming, id)
		}
	}
}
