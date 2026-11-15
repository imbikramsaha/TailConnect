package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/sysinfo"
)

// Access levels (mirrors store.Access*; kept here to avoid an import cycle).
const (
	AccessFull  = "full"
	AccessPorts = "ports"
	AccessStats = "stats"
)

func rank(a string) int {
	switch a {
	case AccessFull:
		return 3
	case AccessPorts:
		return 2
	case AccessStats:
		return 1
	}
	return 0
}

// Identifier resolves the tailnet identity behind a remote address.
type Identifier interface {
	Identify(ctx context.Context, remoteAddr string) (Caller, error)
}

// Ops performs local operations for callers (implemented by hostops).
type Ops interface {
	Services(ctx context.Context) ([]Service, error)
	Processes(ctx context.Context) ([]Process, error)
	Kill(ctx context.Context, pid int) error
	Containers(ctx context.Context) ([]Container, error)
	ContainerAction(ctx context.Context, id, action string) error
	Wake(macs []string, broadcast string) error
	GetClipboard(ctx context.Context) (string, error)
	SetClipboard(ctx context.Context, text string) error
	Receive(name string, r io.Reader) (path string, size int64, err error)
}

// Host is implemented by the app core; it owns trust decisions.
type Host interface {
	Hello(c Caller, access string) Hello
	// Access returns the caller's level ("" = not paired).
	Access(nodeID string) string
	// AllowPort reports whether nodeID may tunnel to a local port.
	AllowPort(nodeID string, port int) bool
	HandlePair(ctx context.Context, c Caller, req PairRequest) PairResponse
	PairStatus(c Caller, id string) PairResponse
	Unpair(c Caller) error
	// Event records/notifies an access event (file received, etc.).
	Event(c Caller, kind, detail string)
}

// Server serves the agent API.
type Server struct {
	ID   Identifier
	Host Host
	Ops  Ops
	Logf func(format string, args ...any)
	srv  *http.Server
}

// Serve serves the API on ln until Close.
func (s *Server) Serve(ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", s.hello)
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("GET /v1/pair/{id}", s.pairStatus)
	mux.HandleFunc("POST /v1/unpair", s.unpair)
	mux.HandleFunc("GET /v1/stats", s.need(AccessStats, s.stats))
	mux.HandleFunc("GET /v1/processes", s.need(AccessStats, s.processes))
	mux.HandleFunc("POST /v1/processes/{pid}/kill", s.need(AccessFull, s.kill))
	mux.HandleFunc("GET /v1/services", s.need(AccessPorts, s.services))
	mux.HandleFunc("GET /v1/containers", s.need(AccessPorts, s.containers))
	mux.HandleFunc("POST /v1/containers/{id}/{action}", s.need(AccessFull, s.containerAction))
	mux.HandleFunc("POST /v1/wake", s.need(AccessPorts, s.wake))
	mux.HandleFunc("GET /v1/speed", s.need(AccessPorts, s.speed))
	mux.HandleFunc("POST /v1/files", s.need(AccessFull, s.receiveFile))
	mux.HandleFunc("GET /v1/clipboard", s.need(AccessFull, s.getClipboard))
	mux.HandleFunc("PUT /v1/clipboard", s.need(AccessFull, s.setClipboard))
	mux.HandleFunc("GET /v1/tunnel", s.need(AccessPorts, s.tunnel))
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	err := s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

func (s *Server) caller(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c, err := s.ID.Identify(r.Context(), r.RemoteAddr)
	if err != nil || c.NodeID == "" {
		http.Error(w, "unknown caller", http.StatusForbidden)
		return Caller{}, false
	}
	return c, true
}

type handler func(w http.ResponseWriter, r *http.Request, c Caller, access string)

// need wraps h so only callers with at least level reach it.
func (s *Server) need(level string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.caller(w, r)
		if !ok {
			return
		}
		a := s.Host.Access(c.NodeID)
		if a == "" {
			http.Error(w, "not paired with this device", http.StatusForbidden)
			return
		}
		if rank(a) < rank(level) {
			http.Error(w, "this device only allows "+describe(a)+" for you", http.StatusForbidden)
			return
		}
		h(w, r, c, a)
	}
}

func describe(a string) string {
	switch a {
	case AccessStats:
		return "the resource monitor"
	case AccessPorts:
		return "ports and the resource monitor"
	}
	return "full access"
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	c, ok := s.caller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.Host.Hello(c, s.Host.Access(c.NodeID)))
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	c, ok := s.caller(w, r)
	if !ok {
		return
	}
	var req PairRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	resp := s.Host.HandlePair(r.Context(), c, req)
	s.logf("agent: pair request from %s (%s): %s", c.Name, c.LoginName, resp.Status)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := s.caller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.Host.PairStatus(c, r.PathValue("id")))
}

func (s *Server) unpair(w http.ResponseWriter, r *http.Request) {
	c, ok := s.caller(w, r)
	if !ok {
		return
	}
	if err := s.Host.Unpair(c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request, _ Caller, _ string) {
	st, err := sysinfo.Collect(r.Context(), 400*time.Millisecond)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) processes(w http.ResponseWriter, r *http.Request, _ Caller, access string) {
	ps, err := s.Ops.Processes(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if access != AccessFull { // command lines can contain secrets
		for i := range ps {
			ps[i].Cmd = ""
		}
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) kill(w http.ResponseWriter, r *http.Request, c Caller, _ string) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid pid"))
		return
	}
	if err := s.Ops.Kill(r.Context(), pid); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.Host.Event(c, "kill", strconv.Itoa(pid))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) services(w http.ResponseWriter, r *http.Request, c Caller, _ string) {
	svcs, err := s.Ops.Services(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := svcs[:0]
	for _, sv := range svcs {
		if s.Host.AllowPort(c.NodeID, sv.Port) {
			out = append(out, sv)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) containers(w http.ResponseWriter, r *http.Request, _ Caller, _ string) {
	cs, err := s.Ops.Containers(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) containerAction(w http.ResponseWriter, r *http.Request, c Caller, _ string) {
	id, action := r.PathValue("id"), r.PathValue("action")
	if err := s.Ops.ContainerAction(r.Context(), id, action); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.Host.Event(c, "docker", action+" "+id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// WakeRequest asks a device to send Wake-on-LAN packets on its LAN.
type WakeRequest struct {
	MACs      []string `json:"macs"`
	Broadcast string   `json:"broadcast"`
}

func (s *Server) wake(w http.ResponseWriter, r *http.Request, c Caller, _ string) {
	var req WakeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil || len(req.MACs) == 0 || len(req.MACs) > 8 {
		writeErr(w, http.StatusBadRequest, errors.New("bad wake request"))
		return
	}
	if err := s.Ops.Wake(req.MACs, req.Broadcast); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.Host.Event(c, "wake", strings.Join(req.MACs, ","))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// MaxSpeedBytes caps a speed test download.
const MaxSpeedBytes = 256 << 20

func (s *Server) speed(w http.ResponseWriter, r *http.Request, _ Caller, _ string) {
	n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	if err != nil || n <= 0 || n > MaxSpeedBytes {
		writeErr(w, http.StatusBadRequest, errors.New("bytes must be 1.."+strconv.Itoa(MaxSpeedBytes)))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	buf := make([]byte, 64<<10)
	for n > 0 {
		k := int64(len(buf))
		if n < k {
			k = n
		}
		if _, err := w.Write(buf[:k]); err != nil {
			return
		}
		n -= k
	}
}

func (s *Server) receiveFile(w http.ResponseWriter, r *http.Request, c Caller, _ string) {
	name := r.URL.Query().Get("name")
	path, size, err := s.Ops.Receive(name, r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.Host.Event(c, "file", path)
	writeJSON(w, http.StatusOK, FileResult{Path: path, Size: size})
}

func (s *Server) getClipboard(w http.ResponseWriter, r *http.Request, _ Caller, _ string) {
	t, err := s.Ops.GetClipboard(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, Clipboard{Text: t})
}

func (s *Server) setClipboard(w http.ResponseWriter, r *http.Request, c Caller, _ string) {
	var cb Clipboard
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&cb); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Ops.SetClipboard(r.Context(), cb.Text); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	s.Host.Event(c, "clipboard", strconv.Itoa(len(cb.Text))+" characters")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// tunnel upgrades the request into a raw TCP stream to a loopback port.
func (s *Server) tunnel(w http.ResponseWriter, r *http.Request, c Caller, _ string) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), tunnelProto) {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || !netx.ValidPort(port) {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	if !s.Host.AllowPort(c.NodeID, port) {
		http.Error(w, "this device doesn't allow you to reach port "+strconv.Itoa(port), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	up, err := netx.DialLoopback(r.Context(), port)
	if err != nil {
		http.Error(w, "nothing is listening on port "+strconv.Itoa(port), http.StatusBadGateway)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err := brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + tunnelProto + "\r\n\r\n"); err != nil {
		conn.Close()
		up.Close()
		return
	}
	if err := brw.Flush(); err != nil {
		conn.Close()
		up.Close()
		return
	}
	s.logf("agent: tunnel %s -> localhost:%d", c.Name, port)
	go netx.Pipe(&netx.BufConn{Conn: conn, R: brw.Reader}, up)
}
