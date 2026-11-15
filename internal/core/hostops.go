package core

import (
	"context"
	"errors"
	"io"

	"github.com/imbikramsaha/tailconnect/internal/agent"
	"github.com/imbikramsaha/tailconnect/internal/hostops"
)

// hostOps adapts package hostops to agent.Ops.
type hostOps struct{ inbox string }

func (hostOps) Services(ctx context.Context) ([]agent.Service, error)  { return hostops.Services(ctx) }
func (hostOps) Processes(ctx context.Context) ([]agent.Process, error) { return hostops.Processes(ctx) }
func (hostOps) Kill(ctx context.Context, pid int) error                { return hostops.Kill(ctx, pid) }
func (hostOps) Containers(ctx context.Context) ([]agent.Container, error) {
	return hostops.Containers(ctx)
}
func (hostOps) ContainerAction(ctx context.Context, id, action string) error {
	return hostops.ContainerAction(ctx, id, action)
}

// Wake only broadcasts on this machine's own networks (a peer must not be
// able to make us send UDP to arbitrary addresses).
func (hostOps) Wake(macs []string, bcast string) error {
	if bcast != "" && bcast != "255.255.255.255" {
		_, lans := hostops.LANInfo()
		ok := false
		for _, l := range lans {
			if broadcastOf(l) == bcast {
				ok = true
			}
		}
		if !ok {
			return errors.New("not a broadcast address of this device's network")
		}
	}
	return hostops.Wake(macs, bcast)
}
func (hostOps) GetClipboard(ctx context.Context) (string, error) {
	return hostops.GetClipboard(ctx)
}
func (hostOps) SetClipboard(ctx context.Context, t string) error { return hostops.SetClipboard(ctx, t) }
func (h hostOps) Receive(name string, r io.Reader) (string, int64, error) {
	dir := h.inbox
	if dir == "" {
		var err error
		if dir, err = hostops.InboxDir(); err != nil {
			return "", 0, err
		}
	}
	return hostops.Receive(dir, name, r)
}
