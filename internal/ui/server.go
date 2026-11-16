// Package ui serves TailConnect's local control API and the embedded web UI
// on 127.0.0.1. Every API request carries a bearer header: the per-run token
// (CLI) or a window session obtained by exchanging a single-use ticket. There
// are deliberately no cookies (browsers share 127.0.0.1 cookies across
// ports). Mutating requests also need a custom header, and requests from
// other sites are refused, so other origins cannot forge them.
package ui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
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
	"sync"
	"time"

	"golang.org/x/net/netutil"

	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/icon"
	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/logx"
	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/sshx"
	"github.com/imbikramsaha/tailconnect/internal/store"
)

//go:embed static
var staticFS embed.FS

const (
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
	// Restart relaunches the app (nil = unsupported).
	Restart func() error

	port int

	smu    sync.Mutex // guards srv and closed (Serve and Close race at shutdown)
	srv    *http.Server
	closed bool

	tmu      sync.Mutex
	tickets  map[string]time.Time // one-time window sign-in tickets
	sessions map[string]session   // UI window sessions (never the CLI token)
	useSeq   uint64               // orders session use (clocks can tie)
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
	if s.port == 0 { // callers normally Bind first, so LoginURL works at once
		s.Bind(ln)
	}
	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(static))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth", s.auth)
	mux.HandleFunc("POST /api/session", s.newSessionHandler) // ticket -> session; the only unauthenticated API
	mux.HandleFunc("GET /ping", s.ping)
	mux.HandleFunc("GET /icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(icon.PNG(true))
	})
	mux.Handle("/", s.page(files)) // methodless so the more specific "/api/" wins

	api := http.NewServeMux()
	api.HandleFunc("GET /api/state", s.state)
	api.HandleFunc("GET /api/events", s.eventsSSE)
	api.HandleFunc("POST /api/show", s.show)
	api.HandleFunc("POST /api/quit", s.quit)
	api.HandleFunc("POST /api/restart", s.restart)
	api.HandleFunc("POST /api/login", s.login)
	api.HandleFunc("POST /api/logout", s.logout)
	api.HandleFunc("POST /api/devices/ssh", s.addSSH)
	api.HandleFunc("PATCH /api/devices/{id}", s.renameDevice)
	api.HandleFunc("PUT /api/devices/{id}/screen", s.setScreenChoice)
	api.HandleFunc("POST /api/screen/setup", s.screenSetup)
	api.HandleFunc("POST /api/screen/permissions", s.screenPermissions)
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
	api.HandleFunc("PUT /api/trusted/{node}", s.setAccess)
	api.HandleFunc("GET /api/devices/{id}/services", s.services)
	api.HandleFunc("POST /api/devices/{id}/forward", s.forward)
	api.HandleFunc("GET /api/devices/{id}/processes", s.processes)
	api.HandleFunc("POST /api/devices/{id}/processes/{pid}/kill", s.kill)
	api.HandleFunc("GET /api/devices/{id}/containers", s.containers)
	api.HandleFunc("POST /api/devices/{id}/containers/{cid}/{action}", s.containerAction)
	api.HandleFunc("POST /api/devices/{id}/wake", s.wake)
	api.HandleFunc("POST /api/devices/{id}/remote-desktop", s.remoteDesktop)
	api.HandleFunc("GET /api/devices/{id}/diagnose", s.diagnose)
	api.HandleFunc("POST /api/devices/{id}/files", s.sendFile)
	api.HandleFunc("POST /api/devices/{id}/clipboard/{dir}", s.clipboard)
	api.HandleFunc("GET /api/activity", s.activity)
	api.HandleFunc("POST /api/report", s.report)
	api.HandleFunc("POST /api/update/check", s.updateCheck)
	api.HandleFunc("POST /api/update/install", s.updateInstall)
	api.HandleFunc("DELETE /api/session", s.endSession)
	mux.Handle("/api/", s.guard(api))

	srv := &http.Server{Handler: s.hostCheck(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	s.smu.Lock()
	if s.closed {
		s.smu.Unlock()
		ln.Close()
		return nil
	}
	s.srv = srv
	s.smu.Unlock()
	// Any local user can connect to loopback: cap connections so idle ones
	// can't exhaust file descriptors and lock the owner out.
	err := srv.Serve(netutil.LimitListener(ln, maxConns))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Port is the bound port.
func (s *Server) Port() int { return s.port }

// Close stops the server.
func (s *Server) Close() error {
	s.smu.Lock()
	s.closed = true
	srv := s.srv
	s.smu.Unlock()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// LoginURL returns a single-use URL (valid two minutes) that signs a UI
// window in and opens route. The long-lived token never appears in URLs or
// process arguments.
func (s *Server) LoginURL(route string) string {
	t := randomHex(16)
	s.tmu.Lock()
	if s.tickets == nil {
		s.tickets = map[string]time.Time{}
	}
	for k, exp := range s.tickets {
		if time.Now().After(exp) {
			delete(s.tickets, k)
		}
	}
	s.tickets[t] = time.Now().Add(2 * time.Minute)
	s.tmu.Unlock()
	u := fmt.Sprintf("http://127.0.0.1:%d/auth?t=%s", s.port, t)
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
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		// API calls only come from our own page (or the CLI, which sends no
		// Sec-Fetch headers). Reject other sites — including other ports on
		// 127.0.0.1, which browsers consider "same-site".
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if v := r.Header.Get("Sec-Fetch-Site"); v != "" && v != "same-origin" {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authed accepts the CLI token or a window session, sent as a bearer header
// only. There is deliberately no cookie: browsers share 127.0.0.1 cookies
// across ports, so any other local web server could receive it.
func (s *Server) authed(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	tok := strings.TrimPrefix(h, "Bearer ")
	if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1 {
		return true
	}
	if len(tok) != 64 {
		return false
	}
	s.tmu.Lock()
	defer s.tmu.Unlock()
	ss, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if time.Since(ss.last) > sessionIdle {
		delete(s.sessions, tok)
		return false
	}
	s.useSeq++
	s.sessions[tok] = session{last: time.Now(), seq: s.useSeq}
	return true
}

// session records a window session's last use: the time (for expiry) and a
// sequence number (for least-recently-used eviction; coarse clocks, as on
// Windows, give many uses the same time).
type session struct {
	last time.Time
	seq  uint64
}

const (
	// An open window refreshes every 15 seconds, so only sessions of closed
	// windows ever sit idle this long.
	sessionIdle = 12 * time.Hour
	maxSessions = 32
	maxConns    = 128
)

// newSession creates a window session, dropping expired ones and, past
// maxSessions, the least recently used.
func (s *Server) newSession() string {
	sess := randomHex(32)
	now := time.Now()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]session{}
	}
	for k, ss := range s.sessions {
		if now.Sub(ss.last) > sessionIdle {
			delete(s.sessions, k)
		}
	}
	for len(s.sessions) >= maxSessions {
		var oldest string
		for k, ss := range s.sessions {
			if oldest == "" || ss.seq < s.sessions[oldest].seq {
				oldest = k
			}
		}
		delete(s.sessions, oldest)
	}
	s.useSeq++
	s.sessions[sess] = session{last: now, seq: s.useSeq}
	return sess
}

// ping lets the CLI check it's talking to the real TailConnect before it
// sends its token: the answer is an HMAC of the caller's nonce, keyed with
// the token (a program squatting on the port can't compute it).
func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	n := r.URL.Query().Get("n")
	if len(n) < 16 || len(n) > 128 {
		http.Error(w, "bad nonce", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"mac": PingMAC(s.Token, n)})
}

// PingMAC is the expected /ping answer for nonce under token.
func PingMAC(token, nonce string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("tailconnect-ping\n" + nonce))
	return hex.EncodeToString(m.Sum(nil))
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

// auth checks a sign-in ticket and hands it to the page in the URL fragment
// (never sent to a server or in a Referer). The page then exchanges it for a
// session with POST /api/session, so the session itself never appears in a
// URL — and so never in browser history or session-restore files. The ticket
// is single-use, so a copy left in history is worthless.
func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	s.tmu.Lock()
	exp, ok := s.tickets[t]
	s.tmu.Unlock()
	if t == "" || !ok || time.Now().After(exp) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, signedOutPage)
		return
	}
	route := strings.TrimPrefix(r.URL.Query().Get("next"), "#")
	if !strings.HasPrefix(route, "/") || strings.ContainsAny(route, "\r\n&#?\\ ") {
		route = ""
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/#t="+t+"&r="+url.QueryEscape(route), http.StatusFound)
}

// newSessionHandler exchanges a single-use ticket for a window session.
// hostCheck has already refused other sites (Sec-Fetch-Site) and hosts.
func (s *Server) newSessionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) == "" {
		http.Error(w, "missing "+csrfHeader+" header", http.StatusForbidden)
		return
	}
	var body struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.Ticket == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.tmu.Lock()
	exp, ok := s.tickets[body.Ticket]
	delete(s.tickets, body.Ticket)
	s.tmu.Unlock()
	if !ok || time.Now().After(exp) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sess := s.newSession()
	if s.Logs != nil {
		s.Logs.Printf("ui: window signed in (%s)", r.UserAgent())
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{"session": sess})
}

// endSession revokes the caller's own window session (the CLI token can't
// be revoked this way).
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.tmu.Lock()
	delete(s.sessions, tok)
	s.tmu.Unlock()
	w.WriteHeader(http.StatusNoContent)
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
		// The page and its scripts hold no secrets; the API needs a session.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
			"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
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
	var confirm *sshx.ErrConfirmHostKey
	if errors.As(err, &confirm) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "confirm host key", "hostKey": confirm.Fingerprint})
		return
	}
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

func (s *Server) setScreenChoice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Choice string `json:"choice"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Core.SetScreenChoice(r.PathValue("id"), body.Choice); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) screenSetup(w http.ResponseWriter, r *http.Request) {
	if err := s.Core.StartScreenSetup(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

// screenPermissions opens macOS's privacy settings for RustDesk.
func (s *Server) screenPermissions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pane string `json:"pane"`
	}
	_ = decode(r, &body)
	if err := launch.OpenPrivacyPane(body.Pane); err != nil {
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
	var body struct {
		Access string `json:"access"`
	}
	_ = decode(r, &body)
	var approve bool
	switch r.PathValue("decision") {
	case "approve":
		approve = true
	case "deny":
	default:
		writeErr(w, http.StatusNotFound, errors.New("unknown decision"))
		return
	}
	if err := s.Core.ApprovePair(r.PathValue("id"), approve, body.Access); err != nil {
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

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ctxTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func (s *Server) setAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Access string `json:"access"`
		Ports  []int  `json:"ports"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Core.SetAccess(r.PathValue("node"), body.Access, body.Ports); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 20*time.Second)
	defer cancel()
	v, err := s.Core.Services(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port  int    `json:"port"`
		Label string `json:"label"`
		Open  bool   `json:"open"`
	}
	if err := decode(r, &body); err != nil || !netx.ValidPort(body.Port) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid port"))
		return
	}
	ctx, cancel := ctxTimeout(r, 45*time.Second)
	defer cancel()
	run, err := s.Core.Forward(ctx, r.PathValue("id"), body.Port, body.Label, body.Open)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, run)
}

func (s *Server) processes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 20*time.Second)
	defer cancel()
	v, err := s.Core.Processes(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) kill(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid pid"))
		return
	}
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	if err := s.Core.Kill(ctx, r.PathValue("id"), pid); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) containers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 20*time.Second)
	defer cancel()
	v, err := s.Core.Containers(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) containerAction(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 70*time.Second)
	defer cancel()
	if err := s.Core.ContainerAction(ctx, r.PathValue("id"), r.PathValue("cid"), r.PathValue("action")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) wake(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	via, err := s.Core.Wake(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"via": via})
}

func (s *Server) remoteDesktop(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 45*time.Second)
	defer cancel()
	kind, err := s.Core.RemoteDesktop(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"kind": kind})
}

func (s *Server) diagnose(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 60*time.Second)
	defer cancel()
	d, err := s.Core.Diagnose(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, d)
}

func (s *Server) sendFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Core.SendFile(r.Context(), r.PathValue("id"), r.URL.Query().Get("name"), r.ContentLength, r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"path": p})
}

func (s *Server) clipboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	switch r.PathValue("dir") {
	case "push":
		if err := s.Core.PushClipboard(ctx, r.PathValue("id")); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		ok(w)
	case "pull":
		n, err := s.Core.PullClipboard(ctx, r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]int{"chars": n})
	default:
		writeErr(w, http.StatusNotFound, errors.New("unknown direction"))
	}
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Core.Activity(200))
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	p, err := s.Core.ReportBundle(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"path": p})
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	u, err := s.Core.CheckUpdate(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, u)
}

func (s *Server) updateInstall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxTimeout(r, 15*time.Minute)
	defer cancel()
	if err := s.Core.InstallUpdate(ctx, s.Quit); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok(w)
}

func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	if s.Restart == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("restart TailConnect from the menu bar or tray"))
		return
	}
	if err := s.Restart(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ok(w)
}
