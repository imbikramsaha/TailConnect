package ui

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

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

	if r, body := do(t, "GET", base+"/", nil); r.StatusCode != 401 || !strings.Contains(body, "not signed in") {
		t.Errorf("unauthenticated page: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", base+"/api/state", nil); r.StatusCode != 401 {
		t.Errorf("unauthenticated api: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", base+"/auth?t=wrong", nil); r.StatusCode != 401 {
		t.Errorf("bad ticket: %d", r.StatusCode)
	}
	// The long-lived API token is never accepted as a sign-in ticket.
	if r, _ := do(t, "GET", base+"/auth?t="+s.Token, nil); r.StatusCode != 401 {
		t.Errorf("token accepted as ticket: %d", r.StatusCode)
	}
	login := s.LoginURL("#/settings")
	if strings.Contains(login, s.Token) {
		t.Fatal("login URL contains the API token")
	}
	r, _ := do(t, "GET", login, nil)
	if r.StatusCode != 302 || r.Header.Get("Location") != "/#/settings" {
		t.Fatalf("auth redirect: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if r2, _ := do(t, "GET", login, nil); r2.StatusCode != 401 {
		t.Errorf("ticket reused: %d", r2.StatusCode)
	}
	var cookie string
	for _, c := range r.Cookies() {
		if c.Name == cookieName {
			cookie = c.Name + "=" + c.Value
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie flags: %+v", c)
			}
		}
	}
	withCookie := map[string]string{"Cookie": cookie}
	if r, body := do(t, "GET", base+"/", withCookie); r.StatusCode != 200 || !strings.Contains(body, "app.js") {
		t.Errorf("index: %d", r.StatusCode)
	}
	if r, body := do(t, "GET", base+"/app.js", withCookie); r.StatusCode != 200 || !strings.Contains(body, "TailConnect") {
		t.Errorf("app.js: %d", r.StatusCode)
	}
	if r, _ := do(t, "POST", base+"/", withCookie); r.StatusCode != 405 {
		t.Errorf("POST page: %d", r.StatusCode)
	}
	r, body := do(t, "GET", base+"/api/state", bearer)
	if r.StatusCode != 200 {
		t.Fatalf("state: %d %s", r.StatusCode, body)
	}
	var v core.View
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.Version != "test" {
		t.Errorf("state body: %v %+v", err, v.Version)
	}
	// Mutations need the CSRF header, even with a valid cookie.
	if r, _ := do(t, "PUT", base+"/api/settings", withCookie); r.StatusCode != 403 {
		t.Errorf("missing csrf header: %d", r.StatusCode)
	}
	h := map[string]string{"Cookie": cookie, csrfHeader: "1"}
	if r, body := do(t, "PUT", base+"/api/settings", h); r.StatusCode != 200 {
		t.Errorf("settings: %d %s", r.StatusCode, body)
	}
	// DNS-rebinding defence.
	if r, _ := do(t, "GET", base+"/api/state", map[string]string{"Authorization": bearer["Authorization"], "Host": "evil.example:1234"}); r.StatusCode != 403 {
		t.Errorf("foreign host: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", base+"/icon.png", nil); r.StatusCode != 200 {
		t.Errorf("icon: %d", r.StatusCode)
	}
}
