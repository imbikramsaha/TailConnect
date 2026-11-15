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

// Identifier resolves the tailnet identity behind a remote address.
type Identifier interface {
	Identify(ctx context.Context, remoteAddr string) (Caller, error)
}

// Host is implemented by the app core; it owns trust decisions.
type Host interface {
	Hello(trusted bool) Hello
	IsTrusted(nodeID string) bool
	HandlePair(ctx context.Context, c Caller, req PairRequest) PairResponse
	PairStatus(c Caller, id string) PairResponse
	Unpair(c Caller) error
}

// Server serves the agent API.
type Server struct {
	ID   Identifier
	Host Host
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
	mux.HandleFunc("GET /v1/stats", s.stats)
	mux.HandleFunc("GET /v1/tunnel", s.tunnel)
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

func (s *Server) caller(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c, err := s.ID.Identify(r.Context(), r.RemoteAddr)
	if err != nil || c.NodeID == "" {
		http.Error(w, "unknown caller", http.StatusForbidden)
		return Caller{}, false
	}
	return c, true
}

func (s *Server) trusted(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c, ok := s.caller(w, r)
	if !ok {
		return c, false
	}
	if !s.Host.IsTrusted(c.NodeID) {
		http.Error(w, "not paired with this device", http.StatusForbidden)
		return c, false
	}
	return c, true
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	c, ok := s.caller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.Host.Hello(s.Host.IsTrusted(c.NodeID)))
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

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trusted(w, r); !ok {
		return
	}
	st, err := sysinfo.Collect(r.Context(), 400*time.Millisecond)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// tunnel upgrades the request into a raw TCP stream to a loopback port.
func (s *Server) tunnel(w http.ResponseWriter, r *http.Request) {
	c, ok := s.trusted(w, r)
	if !ok {
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), tunnelProto) {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || !netx.ValidPort(port) {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	up, err := netx.DialLoopback(r.Context(), port)
	if err != nil {
		http.Error(w, "nothing is listening on port "+strconv.Itoa(port)+" on "+s.Host.Hello(true).Name, http.StatusBadGateway)
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
