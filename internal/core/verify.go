package core

import (
	"context"
	"errors"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

// errUnverified: something answered for a paired device without its key.
var errUnverified = errors.New("couldn't verify this device's identity: something other than TailConnect answered for it. " +
	"If TailConnect was reset on that device, remove it here and pair again")

// verifiedHello fetches a device's Hello and checks who answered. Every
// program and OS user on a machine shares its Tailscale identity, so while
// TailConnect isn't running there another one could answer on its ports.
// TailConnect signs its answers with its device key:
//   - a device that has signed before (its key is pinned) must sign again;
//   - a key the device paired with on this side is accepted (and pinned);
//   - the first signed answer from a paired device pins its key.
//
// Devices on older versions don't sign yet and are accepted as before
// until they do.
func (c *Core) verifiedHello(ctx context.Context, nodeID, ip string) (*agent.Hello, error) {
	h, proof, err := c.agentCli.Hello(ctx, ip)
	if err != nil {
		return nil, err
	}
	st := c.store.Get()
	i := st.FindDeviceByNode(nodeID)
	pinned := ""
	if i >= 0 {
		pinned = st.Devices[i].PeerKey
	}
	approved := ""
	if t, ok := st.Trust(nodeID); ok {
		approved = t.PublicKey
	}
	switch {
	case pinned != "" && proof(pinned):
		return h, nil
	case approved != "" && proof(approved):
		if i >= 0 && pinned != approved {
			c.pinPeerKey(nodeID, approved)
		}
		return h, nil
	case pinned != "":
		c.logs.Printf("refused an unverified answer for %s (%s)", st.Devices[i].Name, ip)
		return nil, errUnverified
	case i >= 0 && h.Trusted && h.DeviceKey != "" && proof(h.DeviceKey):
		c.pinPeerKey(nodeID, h.DeviceKey)
	}
	return h, nil
}

func (c *Core) pinPeerKey(nodeID, key string) {
	_ = c.store.Update(func(st *store.State) error {
		if i := st.FindDeviceByNode(nodeID); i >= 0 && st.Devices[i].PeerKey != key {
			st.Devices[i].PeerKey = key
			return nil
		}
		return errUnchanged
	})
}

// verifyPeer checks a paired device's identity right before sending it
// data (files, clipboard).
func (c *Core) verifyPeer(ctx context.Context, dev store.Device, ip string) error {
	vctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, err := c.verifiedHello(vctx, dev.NodeID, ip)
	return err
}
