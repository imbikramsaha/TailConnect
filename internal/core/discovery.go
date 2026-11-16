package core

import (
	"context"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/tailnet"
)

// peerInfo is what we know about a tailnet node.
type peerInfo struct {
	NodeID    string
	HostName  string
	DNSName   string
	OS        string
	IP        string
	Online    bool
	Link      string
	UserID    int64
	LoginName string
	Tagged    bool
	Hello     *agent.Hello // non-nil if the node runs TailConnect
	ProbedAt  time.Time
	ProbeErr  string
}

// Default probe intervals (overridable in Options for tests).
const (
	defaultProbeTailConnect = 15 * time.Second // re-probe known TailConnect peers
	defaultProbeOther       = 30 * time.Second // re-probe nodes without TailConnect
)

func (c *Core) discoveryLoop() {
	t := time.NewTicker(c.opts.DiscoveryInterval)
	defer t.Stop()
	c.refresh()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
		c.refresh()
	}
}

func (c *Core) refresh() {
	if c.ctx.Err() != nil {
		return
	}
	if !c.node.Running() {
		c.mu.Lock()
		changed := len(c.peers) > 0
		for _, p := range c.peers {
			p.Online = false
		}
		c.mu.Unlock()
		if changed {
			c.events.Notify()
		}
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	st, err := c.node.Status(ctx)
	cancel()
	if err != nil || st.Self == nil {
		return
	}
	now := time.Now()
	var toProbe []*peerInfo

	c.mu.Lock()
	c.selfUserID = int64(st.Self.UserID)
	c.selfNodeID = string(st.Self.ID)
	c.selfIPs = slices.Clone(st.Self.TailscaleIPs)
	next := make(map[string]*peerInfo, len(st.Peer))
	for _, ps := range st.Peer {
		id := string(ps.ID)
		if id == "" {
			continue
		}
		p := c.peers[id]
		if p == nil {
			p = &peerInfo{NodeID: id}
		}
		if ps.Online && !p.Online {
			p.ProbedAt = time.Time{} // just came online: probe right away
		}
		p.HostName = ps.HostName
		p.DNSName = trimDot(ps.DNSName)
		p.OS = ps.OS
		p.Online = ps.Online
		p.Link = tailnet.LinkDescription(ps)
		p.UserID = int64(ps.UserID)
		p.Tagged = ps.Tags != nil && ps.Tags.Len() > 0
		if u, ok := st.User[ps.UserID]; ok {
			p.LoginName = u.LoginName
		}
		p.IP = ""
		for _, ip := range ps.TailscaleIPs {
			if ip.Is4() {
				p.IP = ip.String()
				break
			}
		}
		if p.IP == "" && len(ps.TailscaleIPs) > 0 {
			p.IP = ps.TailscaleIPs[0].String()
		}
		next[id] = p
		every := c.opts.ProbeOther
		if p.Hello != nil {
			every = c.opts.ProbeTailConnect
		}
		if p.Online && p.IP != "" && now.Sub(p.ProbedAt) >= every {
			p.ProbedAt = now
			toProbe = append(toProbe, p)
		}
		if !p.Online {
			p.Hello = nil
		}
	}
	c.peers = next
	c.mu.Unlock()

	if len(toProbe) > 0 {
		c.probe(toProbe)
		st := c.store.Get()
		for _, p := range toProbe {
			if i := st.FindDeviceByNode(p.NodeID); i >= 0 {
				if pp, ok := c.peer(p.NodeID); ok && pp.Hello != nil && pp.Hello.Trusted {
					c.applyHello(st.Devices[i], pp.Hello)
				}
			}
		}
	}
	c.expirePending()
	c.autoPair()
	c.events.Notify()
}

// probe asks each node for a TailConnect hello, concurrently.
func (c *Core) probe(list []*peerInfo) {
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, p := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(p *peerInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			c.mu.Lock()
			ip := p.IP
			c.mu.Unlock()
			ctx, cancel := context.WithTimeout(c.ctx, 4*time.Second)
			h, err := c.verifiedHello(ctx, p.NodeID, ip)
			cancel()
			c.mu.Lock()
			if err != nil {
				p.Hello = nil
				p.ProbeErr = err.Error()
			} else {
				p.Hello = h
				p.ProbeErr = ""
			}
			c.mu.Unlock()
		}(p)
	}
	wg.Wait()
}

// autoPair pairs with TailConnect devices of the same Tailscale user.
func (c *Core) autoPair() {
	st := c.store.Get()
	var ids []string
	c.mu.Lock()
	for id, p := range c.peers {
		if p.Hello == nil || !p.Online {
			continue
		}
		// A device that already trusts us (e.g. approved our request after
		// we stopped waiting, or after a restart) just needs saving.
		// Only if we asked to pair with it: "trusted" is the peer's own claim.
		_, asked := c.outgoing[id]
		trustsUnsaved := asked && p.Hello.Trusted && st.FindDeviceByNode(id) < 0
		if !trustsUnsaved && (!st.Settings.AutoTrustOwnDevices || p.Tagged || p.UserID == 0 || p.UserID != c.selfUserID) {
			continue
		}
		if st.FindDeviceByNode(id) >= 0 && p.Hello.Trusted {
			continue // already paired both ways
		}
		if slices.Contains(st.Ignored, id) {
			continue
		}
		if o := c.outgoing[id]; o != nil {
			switch {
			case o.Status == outRequesting || o.Status == outWaiting:
				continue // in progress
			case o.Status == outDenied && !p.Hello.Trusted:
				continue // needs a manual decision; retry only once they trust us
			case o.Status == outError && time.Since(o.Updated) < time.Minute:
				continue // back off after failures
			}
		}
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.logs.Printf("auto-pairing with own device %s", id)
		c.startPair(id, true)
	}
}

// peer returns a copy of a peer's info.
func (c *Core) peer(nodeID string) (peerInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.peers[nodeID]
	if !ok {
		return peerInfo{}, false
	}
	cp := *p
	if p.Hello != nil {
		h := *p.Hello
		cp.Hello = &h
	}
	return cp, true
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

// isTailnetHost reports whether host looks like a tailnet address/name.
func (c *Core) isTailnetHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return netip.MustParsePrefix("100.64.0.0/10").Contains(ip) ||
			netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.peers {
		if host == p.HostName || host == p.DNSName || (p.DNSName != "" && host == shortName(p.DNSName)) {
			return true
		}
	}
	return false
}

func shortName(dns string) string {
	for i := 0; i < len(dns); i++ {
		if dns[i] == '.' {
			return dns[:i]
		}
	}
	return dns
}
