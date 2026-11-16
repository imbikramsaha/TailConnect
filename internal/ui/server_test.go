package ui

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/core"
	"github.com/imbikramsaha/tailconnect/internal/store"
	"github.com/imbikramsaha/tailconnect/internal/tailnet"
)

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := core.New(core.Options{
		Version: "test", Store: st, Headless: true,
		Tailnet: tailnet.Config{Dir: filepath.Join(dir, "ts"), InMemory: true},
		SSHDir:  filepath.Join(dir, "ssh"), AuthorizedKeys: filepath.Join(dir, "ak"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Core: c, Token: "secret-token"}
	s.Bind(ln)
	go s.Serve(ln)
	t.Cleanup(func() { s.Close() })
	return s, "http://" + ln.Addr().String()
}

func do(t *testing.T, method, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader("{}"))
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestServerAuthAndRoutes(t *testing.T) {
	s, base := startServer(t)
	bearer := map[string]string{"Authorization": "Bearer " + s.Token}

	// The page shell is public (no secrets); the API is not.
	if r, body := do(t, "GET", base+"/", nil); r.StatusCode != 200 || !strings.Contains(body, "app.js") {
		t.Errorf("index: %d", r.StatusCode)
	} else if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "object-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP: %q", csp)
	}
	if r, _ := do(t, "GET", base+"/api/state", nil); r.StatusCode != 401 {
		t.Errorf("unauthenticated api: %d", r.StatusCode)
	}
	if r, _ := do(t, "POST", base+"/", nil); r.StatusCode != 405 {
		t.Errorf("POST page: %d", r.StatusCode)
	}

	// Sign-in: the single-use ticket goes to the page in the URL fragment;
	// the page exchanges it for a session over POST. No cookie, and the
	// session never appears in a URL.
	if r, _ := do(t, "GET", base+"/auth?t="+s.Token, nil); r.StatusCode != 401 {
		t.Errorf("CLI token accepted as a ticket: %d", r.StatusCode)
	}
	login := s.LoginURL("#/settings")
	if strings.Contains(login, s.Token) {
		t.Fatal("login URL contains the API token")
	}
	r, _ := do(t, "GET", login, nil)
	loc := r.Header.Get("Location")
	if r.StatusCode != 302 || !strings.HasPrefix(loc, "/#t=") || !strings.HasSuffix(loc, "&r=%2Fsettings") {
		t.Fatalf("auth redirect: %d %q", r.StatusCode, loc)
	}
	if len(r.Cookies()) != 0 {
		t.Errorf("sign-in set a cookie: %v", r.Cookies())
	}
	if strings.Contains(loc, s.Token) {
		t.Error("redirect leaks the API token")
	}
	ticket := strings.TrimPrefix(strings.SplitN(loc, "&", 2)[0], "/#t=")
	if r, _ := do(t, "GET", base+"/api/state", map[string]string{"Authorization": "Bearer " + ticket}); r.StatusCode != 401 {
		t.Errorf("a ticket works as a session: %d", r.StatusCode)
	}
	exchange := func(ticket string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest("POST", base+"/api/session", strings.NewReader(`{"ticket":"`+ticket+`"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	if r, _ := exchange(ticket, nil); r.StatusCode != 403 {
		t.Errorf("exchange without CSRF header: %d", r.StatusCode)
	}
	if r, _ := exchange(ticket, map[string]string{csrfHeader: "1", "Sec-Fetch-Site": "same-site"}); r.StatusCode != 403 {
		t.Errorf("exchange from another site: %d", r.StatusCode)
	}
	r, body := exchange(ticket, map[string]string{csrfHeader: "1"})
	var got struct{ Session string }
	if r.StatusCode != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got.Session) != 64 {
		t.Fatalf("exchange: %d %s", r.StatusCode, body)
	}
	if r, _ := exchange(ticket, map[string]string{csrfHeader: "1"}); r.StatusCode != 401 {
		t.Errorf("ticket reused: %d", r.StatusCode)
	}
	sess := got.Session
	if r, _ := do(t, "GET", s.LoginURL("#/x&evil=1"), nil); !strings.HasSuffix(r.Header.Get("Location"), "&r=") {
		t.Errorf("unsafe route passed through: %q", r.Header.Get("Location"))
	}

	withSess := map[string]string{"Authorization": "Bearer " + sess}
	r, body = do(t, "GET", base+"/api/state", withSess)
	if r.StatusCode != 200 {
		t.Fatalf("state with session: %d %s", r.StatusCode, body)
	}
	var v core.View
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.Version != "test" {
		t.Errorf("state body: %v %+v", err, v.Version)
	}
	if r, _ := do(t, "GET", base+"/api/state", map[string]string{"Authorization": "Bearer " + strings.Repeat("0", 64)}); r.StatusCode != 401 {
		t.Errorf("made-up session accepted: %d", r.StatusCode)
	}
	// Mutations need the CSRF header.
	if r, _ := do(t, "PUT", base+"/api/settings", withSess); r.StatusCode != 403 {
		t.Errorf("missing csrf header: %d", r.StatusCode)
	}
	h := map[string]string{"Authorization": "Bearer " + sess, csrfHeader: "1"}
	if r, body := do(t, "PUT", base+"/api/settings", h); r.StatusCode != 200 {
		t.Errorf("settings: %d %s", r.StatusCode, body)
	}
	// Settings are validated.
	req, _ := http.NewRequest("PUT", base+"/api/settings", strings.NewReader(`{"ownDeviceAccess":"root"}`))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 400 {
		t.Errorf("invalid setting accepted: %v %v", err, resp)
	}
	// Other sites — including another port on 127.0.0.1 — are refused.
	for _, site := range []string{"cross-site", "same-site"} {
		hh := map[string]string{"Authorization": "Bearer " + sess, "Sec-Fetch-Site": site}
		if r, _ := do(t, "GET", base+"/api/state", hh); r.StatusCode != 403 {
			t.Errorf("Sec-Fetch-Site %s: %d", site, r.StatusCode)
		}
	}
	// DNS-rebinding defence.
	if r, _ := do(t, "GET", base+"/api/state", map[string]string{"Authorization": bearer["Authorization"], "Host": "evil.example:1234"}); r.StatusCode != 403 {
		t.Errorf("foreign host: %d", r.StatusCode)
	}
	// Ping proves knowledge of the token without revealing it.
	r, body = do(t, "GET", base+"/ping?n=0123456789abcdef0123", nil)
	if r.StatusCode != 200 || !strings.Contains(body, PingMAC(s.Token, "0123456789abcdef0123")) || strings.Contains(body, s.Token) {
		t.Errorf("ping: %d %s", r.StatusCode, body)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, base := startServer(t)
	newSess := func() string {
		r, _ := do(t, "GET", s.LoginURL(""), nil)
		ticket := strings.TrimPrefix(strings.SplitN(r.Header.Get("Location"), "&", 2)[0], "/#t=")
		req, _ := http.NewRequest("POST", base+"/api/session", strings.NewReader(`{"ticket":"`+ticket+`"}`))
		req.Header.Set(csrfHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var got struct{ Session string }
		_ = json.NewDecoder(resp.Body).Decode(&got)
		return got.Session
	}
	ok := func(sess string) bool {
		r, _ := do(t, "GET", base+"/api/state", map[string]string{"Authorization": "Bearer " + sess})
		return r.StatusCode == 200
	}
	// A window can end its own session.
	a := newSess()
	if !ok(a) {
		t.Fatal("new session rejected")
	}
	if r, _ := do(t, "DELETE", base+"/api/session", map[string]string{"Authorization": "Bearer " + a, csrfHeader: "1"}); r.StatusCode != 204 {
		t.Errorf("end session: %d", r.StatusCode)
	}
	if ok(a) {
		t.Error("ended session still works")
	}
	// Sessions are capped; the least recently used go first.
	first := newSess()
	for i := 0; i < maxSessions; i++ {
		newSess()
	}
	if ok(first) {
		t.Error("session cap not enforced")
	}
	// Idle sessions expire.
	b := newSess()
	s.tmu.Lock()
	s.sessions[b] = time.Now().Add(-sessionIdle - time.Minute)
	s.tmu.Unlock()
	if ok(b) {
		t.Error("idle session still works")
	}
	// The CLI token is unaffected by all of this.
	if !ok(s.Token) {
		t.Error("CLI token rejected")
	}
}
