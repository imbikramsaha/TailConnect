// Package ui serves TailConnect's local control API and the embedded web UI
// on 127.0.0.1. Every request must carry the per-run token (bearer header
// for the CLI, a SameSite=Strict cookie for the UI window); mutating requests
// also require a custom header so other origins cannot forge them.
package ui

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/icon"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/logx"
	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

//go:embed static
var staticFS embed.FS

const (
	cookieName = "tc_session"
	csrfHeader = "X-TailConnect"
	// DefaultPort is tried first for the control API.
	DefaultPort = 47474
)

// Server is the local control API + UI.
type Server struct {
	Core  *core.Core
	Token string
	Logs  *logx.Logs
	// ShowUI opens the UI window at a hash route (e.g. "#/settings").
	ShowUI func(route string)
	// Quit asks the app to exit.
	Quit func()

	port int
	srv  *http.Server
}

// Listen binds the control API on loopback, preferring DefaultPort.
func Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(DefaultPort)))
	if err == nil {
		return ln, nil
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// Bind records the listener's port (call before Serve so LoginURL works).
func (s *Server) Bind(ln net.Listener) { s.port = ln.Addr().(*net.TCPAddr).Port }

// Serve serves on ln until Close.
func (s *Server) Serve(ln net.Listener) error {
	s.Bind(ln)
	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(static))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth", s.auth)
	mux.HandleFunc("GET /icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(icon.PNG(true))
	})
	mux.Handle("/", s.page(files)) // methodless so the more specific "/api/" wins

	api := http.NewServeMux()
	api.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"status": "ok"}) })
	api.HandleFunc("GET /api/state", s.state)
	api.HandleFunc("GET /api/events", s.eventsSSE)
	api.HandleFunc("POST /api/show", s.show)
	api.HandleFunc("POST /api/quit", s.quit)
	api.HandleFunc("POST /api/login", s.login)
	api.HandleFunc("POST /api/logout", s.logout)
	api.HandleFunc("POST /api/devices/ssh", s.addSSH)
	api.HandleFunc("PATCH /api/devices/{id}", s.renameDevice)
	api.HandleFunc("DELETE /api/devices/{id}", s.removeDevice)
	api.HandleFunc("POST /api/devices/{id}/connect", s.connect)
	api.HandleFunc("POST /api/devices/{id}/disconnect", s.disconnect)
	api.HandleFunc("POST /api/devices/{id}/pair", s.pair)
	api.HandleFunc("POST /api/devices/{id}/launch", s.launchApp)
	api.HandleFunc("GET /api/devices/{id}/stats", s.stats)
	api.HandleFunc("POST /api/devices/{id}/apps/{app}/start", s.startApp)
	api.HandleFunc("POST /api/devices/{id}/apps/{app}/stop", s.stopApp)
	api.HandleFunc("POST /api/apps", s.saveApp)
	api.HandleFunc("DELETE /api/apps/{id}", s.deleteApp)
	api.HandleFunc("POST /api/pair-requests/{id}/{decision}", s.decidePair)
	api.HandleFunc("DELETE /api/trusted/{node}", s.revoke)
	api.HandleFunc("PUT /api/settings", s.settings)
	api.HandleFunc("POST /api/open-logs", s.openLogs)
	api.HandleFunc("GET /api/proxy", s.proxy)
	mux.Handle("/api/", s.guard(api))

	s.srv = &http.Server{Handler: s.hostCheck(mux), ReadHeaderTimeout: 10 * time.Second}
	err := s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Port is the bound port.
func (s *Server) Port() int { return s.port }

// Close stops the server.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// LoginURL is the URL that authenticates a UI window and opens route.
func (s *Server) LoginURL(route string) string {
	u := fmt.Sprintf("http://127.0.0.1:%d/auth?t=%s", s.port, url.QueryEscape(s.Token))
	if route != "" {
		u += "&next=" + url.QueryEscape(route)
	}
	return u
}

// hostCheck rejects requests whose Host is not our loopback address
// (defends against DNS rebinding).
func (s *Server) hostCheck(next http.Handler) http.Handler {
	ok := map[string]bool{
		"127.0.0.1:" + strconv.Itoa(s.port): true,
		"localhost:" + strconv.Itoa(s.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(r *http.Request) bool {
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimPrefix(h, "Bearer ")
	} else if c, err := r.Cookie(cookieName); err == nil {
		tok = c.Value
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get(csrfHeader) == "" {
			http.Error(w, "missing "+csrfHeader+" header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth exchanges the one-time URL token for a session cookie.
func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	if subtle.ConstantTimeCompare([]byte(t), []byte(s.Token)) != 1 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "#/") || strings.ContainsAny(next, "\r\n") {
		next = ""
	}
	http.Redirect(w, r, "/"+next, http.StatusFound)
}

const signedOutPage = `<!doctype html><meta charset="utf-8"><title>TailConnect</title>
<body style="font:15px system-ui;display:grid;place-items:center;height:90vh;color:#555">
<div style="text-align:center"><img src="/icon.png" width="56" alt=""><h2>TailConnect</h2>
<p>This window is not signed in. Open TailConnect from the tray icon,<br>or run <code>tailconnect open</code>.</p></div></body>`

func (s *Server) page(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.authed(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, signedOutPage)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func ok(w http.ResponseWriter) { writeJSON(w, map[string]string{"status": "ok"}) }

// ---- handlers ----

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Core.Snapshot(r.Context()))
}

func (s *Server) eventsSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, unsub := s.Core.Events().Subscribe()
	defer unsub()
	_, _ = io.WriteString(w, "event: change\ndata: {}\n\n")
	fl.Flush()
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
		case <-ch:
			// Coalesce bursts of changes.
			time.Sleep(150 * time.Millisecond)
			select {
			case <-ch:
			default:
			}
			_, _ = io.WriteString(w, "event: change\ndata: {}\n\n")
		}
		fl.Flush()
	}
}

func (s *Server) show(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Route string `json:"route"`
	}
	_ = decode(r, &body)
	if s.ShowUI != nil {
		go s.ShowUI(body.Route)
	}
	ok(w)
}

func (s *Server) quit(w http.ResponseWriter, r *http.Request) {
	ok(w)
	if s.Quit != nil {
		go func() {
			time.Sleep(200 * time.Millisecond)
			s.Quit()
		}()
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	u, err := s.Core.Login(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"authURL": u})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.Logout(r.Context()); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	ok(w)
}

func (s *Server) addSSH(w http.ResponseWriter, r *http.Request) {
	var req core.AddSSHRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	dev, err := s.Core.AddSSHDevice(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, dev)
}

func (s *Server) renameDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Core.RenameDevice(r.PathValue("id"), body.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) removeDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.RemoveDevice(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.Connect(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	s.Core.Disconnect(r.PathValue("id"))
	ok(w)
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.PairDevice(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) launchApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		App string `json:"app"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Core.Launch(r.Context(), r.PathValue("id"), body.App); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	st, err := s.Core.Stats(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) startApp(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	run, err := s.Core.StartApp(ctx, r.PathValue("id"), r.PathValue("app"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, run)
}

func (s *Server) stopApp(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.StopApp(r.PathValue("id"), r.PathValue("app")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) saveApp(w http.ResponseWriter, r *http.Request) {
	var a store.App
	if err := decode(r, &a); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a, err := s.Core.SaveApp(a)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, a)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.DeleteApp(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) decidePair(w http.ResponseWriter, r *http.Request) {
	var approve bool
	switch r.PathValue("decision") {
	case "approve":
		approve = true
	case "deny":
	default:
		writeErr(w, http.StatusNotFound, errors.New("unknown decision"))
		return
	}
	if err := s.Core.ApprovePair(r.PathValue("id"), approve); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.RevokeTrust(r.PathValue("node")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var perr error
	err = s.Core.UpdateSettings(func(st *store.Settings) {
		// Unmarshal onto the current settings: only provided fields change.
		perr = json.Unmarshal(raw, st)
	})
	if perr != nil {
		writeErr(w, http.StatusBadRequest, perr)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) openLogs(w http.ResponseWriter, r *http.Request) {
	if s.Logs == nil || s.Logs.Dir == "" {
		writeErr(w, http.StatusNotFound, errors.New("no log directory"))
		return
	}
	if err := launch.OpenPath(s.Logs.Dir); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ok(w)
}

// proxy upgrades to a raw stream to a device port (ssh ProxyCommand).
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "tailconnect-proxy") {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || !netx.ValidPort(port) {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	up, err := s.Core.DialDevice(ctx, r.URL.Query().Get("device"), port)
	cancel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, okh := w.(http.Hijacker)
	if !okh {
		up.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tailconnect-proxy\r\n\r\n")
	if err := brw.Flush(); err != nil {
		conn.Close()
		up.Close()
		return
	}
	go netx.Pipe(&netx.BufConn{Conn: conn, R: brw.Reader}, up)
}
