// Package logx sets up TailConnect's log file (with simple size rotation)
// and a separate session log recording connects, launches and pairings.
package logx

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

const maxLogSize = 10 << 20 // rotate at 10 MiB

// Logs bundles the application and session loggers.
type Logs struct {
	Dir     string
	App     *log.Logger
	Session *log.Logger
	// Verbose receives the embedded Tailscale backend's debug output.
	Verbose *log.Logger
	files   []*os.File
}

func open(dir, name string) (*os.File, error) {
	p := filepath.Join(dir, name)
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxLogSize {
		_ = os.Rename(p, p+".1")
	}
	return os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// Open creates loggers writing into dir. If echo is non-nil, app log lines
// are also written there (e.g. stderr in headless mode).
func Open(dir string, echo io.Writer) (*Logs, error) {
	l := &Logs{Dir: dir}
	app, err := open(dir, "tailconnect.log")
	if err != nil {
		return nil, err
	}
	sess, err := open(dir, "session.log")
	if err != nil {
		app.Close()
		return nil, err
	}
	ts, err := open(dir, "tailscale.log")
	if err != nil {
		app.Close()
		sess.Close()
		return nil, err
	}
	l.files = []*os.File{app, sess, ts}
	var aw io.Writer = app
	if echo != nil {
		aw = io.MultiWriter(app, echo)
	}
	l.App = log.New(aw, "", log.LstdFlags)
	l.Session = log.New(io.MultiWriter(sess, app), "session: ", log.LstdFlags)
	l.Verbose = log.New(ts, "", log.LstdFlags|log.Lmicroseconds)
	l.App.Printf("---- TailConnect starting %s (pid %d)", time.Now().Format(time.RFC3339), os.Getpid())
	return l, nil
}

// Close closes the underlying files.
func (l *Logs) Close() {
	for _, f := range l.files {
		f.Close()
	}
}

// Discard returns loggers that write nowhere (tests).
func Discard() *Logs {
	d := log.New(io.Discard, "", 0)
	return &Logs{App: d, Session: d, Verbose: d}
}

// Printf logs to the app log.
func (l *Logs) Printf(format string, args ...any) { l.App.Output(2, fmt.Sprintf(format, args...)) }
