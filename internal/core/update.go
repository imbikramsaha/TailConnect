package core

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/update"
)

// UpdateInfo is the updater's state for the UI.
type UpdateInfo struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest,omitempty"`
	Available  bool      `json:"available"`
	URL        string    `json:"url,omitempty"`
	Notes      string    `json:"notes,omitempty"`
	CanInstall bool      `json:"canInstall"`
	Checking   bool      `json:"checking"`
	Installing bool      `json:"installing"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checkedAt,omitzero"`
	DevBuild   bool      `json:"devBuild"`
	rel        *update.Release
}

func (c *Core) updateInfo() UpdateInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := c.update
	u.Current = c.opts.Version
	u.DevBuild = !update.Valid(c.opts.Version)
	u.CanInstall = u.Available && update.CanInstall(c.appExe())
	return u
}

// CheckUpdate looks for a newer release.
func (c *Core) CheckUpdate(ctx context.Context) (UpdateInfo, error) {
	c.mu.Lock()
	c.update.Checking = true
	c.mu.Unlock()
	c.events.Notify()
	rel, err := update.Latest(ctx)
	c.mu.Lock()
	c.update.Checking = false
	c.update.CheckedAt = time.Now()
	wasAvailable := c.update.Available
	if err != nil {
		c.update.Error = err.Error()
	} else {
		c.update.Error = ""
		c.update.Latest, c.update.URL, c.update.Notes, c.update.rel = rel.Version, rel.URL, rel.Notes, rel
		c.update.Available = update.Newer(rel.Version, c.opts.Version)
	}
	now := c.update.Available
	c.mu.Unlock()
	c.events.Notify()
	if now && !wasAvailable {
		c.notify("TailConnect "+rel.Version+" is available", "Open TailConnect to install it.", "#/settings/about")
	}
	return c.updateInfo(), err
}

// InstallUpdate installs the available release (macOS) and relaunches.
func (c *Core) InstallUpdate(ctx context.Context, quit func()) error {
	c.mu.Lock()
	rel := c.update.rel
	if rel == nil || !c.update.Available || c.update.Installing {
		c.mu.Unlock()
		return errors.New("no update to install")
	}
	c.update.Installing = true
	c.update.Error = ""
	c.mu.Unlock()
	c.events.Notify()
	err := update.Install(ctx, rel, c.appExe(), os.Getpid())
	c.mu.Lock()
	c.update.Installing = false
	if err != nil {
		c.update.Error = err.Error()
	}
	c.mu.Unlock()
	c.events.Notify()
	if err != nil {
		return err
	}
	c.logs.Session.Printf("installed update %s; restarting", rel.Version)
	if quit != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			quit()
		}()
	}
	return nil
}

// updateLoop checks for updates daily when enabled (release builds only).
func (c *Core) updateLoop() {
	if !update.Valid(c.opts.Version) || c.opts.Headless {
		return
	}
	select {
	case <-c.ctx.Done():
		return
	case <-time.After(2 * time.Minute): // let startup settle
	}
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if c.store.Get().Settings.CheckUpdates {
			ctx, cancel := context.WithTimeout(c.ctx, time.Minute)
			if _, err := c.CheckUpdate(ctx); err != nil {
				c.logs.Printf("update check: %v", err)
			}
			cancel()
		}
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
	}
}
