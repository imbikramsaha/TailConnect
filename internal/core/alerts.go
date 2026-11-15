package core

import (
	"context"
	"fmt"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/sysinfo"
)

const alertCooldown = 30 * time.Minute

// alertLoop watches this device and connected devices for resource
// problems and notifies (at most once per cooldown per alert).
func (c *Core) alertLoop() {
	t := time.NewTicker(c.opts.AlertInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		a := c.store.Get().Settings.Alerts
		if !a.Enabled {
			continue
		}
		targets := map[string]string{"self": c.selfName}
		c.mu.Lock()
		for id, cs := range c.conns {
			if cs.State == StateConnected {
				targets[id] = id
			}
		}
		c.mu.Unlock()
		for id := range targets {
			name := c.selfName
			if id != "self" {
				d, ok := c.device(id)
				if !ok {
					continue
				}
				name = d.Name
			}
			ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
			s, err := c.Stats(ctx, id)
			cancel()
			if err != nil {
				continue
			}
			for _, al := range CheckAlerts(s, a) {
				c.fireAlert(id, name, al)
			}
		}
	}
}

// Alert is a threshold crossing.
type Alert struct{ Key, Text string }

// CheckAlerts returns the alerts s triggers under thresholds a.
func CheckAlerts(s *sysinfo.Stats, a store.Alerts) []Alert {
	var out []Alert
	if a.MemPct > 0 && s.MemTotal > 0 {
		if p := 100 * float64(s.MemUsed) / float64(s.MemTotal); p >= a.MemPct {
			out = append(out, Alert{"mem", fmt.Sprintf("Memory is %.0f%% full", p)})
		}
	}
	if a.DiskPct > 0 {
		for _, d := range s.Disks {
			if d.Total == 0 {
				continue
			}
			if p := 100 * float64(d.Used) / float64(d.Total); p >= a.DiskPct {
				out = append(out, Alert{"disk:" + d.Mount, fmt.Sprintf("Disk %s is %.0f%% full", d.Mount, p)})
			}
		}
	}
	if a.GPUTempC > 0 {
		for i, g := range s.GPUs {
			if g.TempC >= a.GPUTempC {
				out = append(out, Alert{fmt.Sprintf("gpu%d", i), fmt.Sprintf("%s is at %.0f°C", g.Name, g.TempC)})
			}
		}
	}
	return out
}

func (c *Core) fireAlert(id, name string, al Alert) {
	key := id + "|" + al.Key
	c.mu.Lock()
	last := c.alerts[key]
	if time.Since(last) < alertCooldown {
		c.mu.Unlock()
		return
	}
	c.alerts[key] = time.Now()
	c.mu.Unlock()
	c.logs.Session.Printf("alert on %q: %s", name, al.Text)
	route := "#/"
	if id != "self" {
		route = "#/device/" + id
	}
	c.notify("⚠ "+name, al.Text, route)
}
