package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/v2/internal/auth"
	"github.com/rapando/groundwork/v2/internal/events"
)

const tok = "secret-token"

func newTestServer(t *testing.T) (*httptest.Server, string) {
	ts, host, _ := newAuthServer(t)
	return ts, host
}

// newAuthServer returns the server, its host and the data directory holding auth.json.
func newAuthServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	home := t.TempDir()
	a, err := auth.New(home, "")
	if err != nil {
		t.Fatal(err)
	}
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), events.NewBus(), Info{Root: "/r", Version: "t"}, tok)
	s.SetAuth(a)
	s.SetAPI(func(r chi.Router) {
		r.Get("/workspace", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.info) })
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	s.AllowHost(strings.TrimPrefix(ts.URL, "http://"))
	return ts, strings.TrimPrefix(ts.URL, "http://"), home
}

func do(t *testing.T, method, url string, hdr map[string]string, cookie bool) *http.Response {
	t.Helper()
	c := ""
	if cookie {
		c = tok
	}
	return doBody(t, method, url, hdr, c, "")
}

// doBody sends body with the session cookie value c (none when "").
func doBody(t *testing.T, method, url string, hdr map[string]string, c, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	if c != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c})
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func sessionCookie(t *testing.T, r *http.Response) *http.Cookie {
	t.Helper()
	for _, k := range r.Cookies() {
		if k.Name == cookieName {
			return k
		}
	}
	t.Fatalf("no session cookie (%d)", r.StatusCode)
	return nil
}

func TestRejectsWithoutCredentials(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, p := range []string{"/api/workspace", "/api/events"} {
		if r := do(t, "GET", ts.URL+p, nil, false); r.StatusCode != 401 {
			t.Errorf("%s: got %d want 401", p, r.StatusCode)
		}
		// the token as a cookie is not a browser session
		if r := do(t, "GET", ts.URL+p, nil, true); r.StatusCode != 401 {
			t.Errorf("%s with the token as cookie: got %d want 401", p, r.StatusCode)
		}
	}
	if r := do(t, "GET", ts.URL+"/api/workspace", map[string]string{TokenHeader: "wrong"}, false); r.StatusCode != 401 {
		t.Errorf("wrong header: got %d", r.StatusCode)
	}
}

// The CLI holds the token (from the 0600 file) and sends it as a header.
func TestCLITokenHeaderReachesTheAPI(t *testing.T) {
	ts, _ := newTestServer(t)
	if r := do(t, "GET", ts.URL+"/api/workspace", map[string]string{TokenHeader: tok}, false); r.StatusCode != 200 {
		t.Fatalf("got %d", r.StatusCode)
	}
}

// Signed out, the page loads (it shows the sign-in screen) but carries no token.
func TestSignedOutPageHasNoToken(t *testing.T) {
	ts, _ := newTestServer(t)
	r := do(t, "GET", ts.URL+"/p/x/runs", nil, true)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || strings.Contains(string(b), tok) || strings.Contains(string(b), "groundwork-token") {
		t.Fatalf("status %d body %s", r.StatusCode, b)
	}
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the page must not be cached")
	}
}

func TestPasswordSetupSignInAndSignOut(t *testing.T) {
	ts, host, home := newAuthServer(t)
	origin := map[string]string{"Origin": "http://" + host, "Content-Type": "application/json"}
	status := func(c string) map[string]any {
		r := doBody(t, "GET", ts.URL+"/auth/status", nil, c, "")
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		return m
	}
	if m := status(""); m["configured"] != false || m["setup_allowed"] != true || m["signed_in"] != false {
		t.Fatalf("fresh: %v", m)
	}
	// CSRF: setup from another site is refused
	if r := doBody(t, "POST", ts.URL+"/auth/password/setup", map[string]string{"Origin": "http://evil.example"}, "", `{"password":"correct horse"}`); r.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", r.StatusCode)
	}
	if r := doBody(t, "POST", ts.URL+"/auth/password/setup", origin, "", `{"password":"short"}`); r.StatusCode != 422 {
		t.Fatalf("short: %d", r.StatusCode)
	}
	r := doBody(t, "POST", ts.URL+"/auth/password/setup", origin, "", `{"password":"correct horse"}`)
	c := sessionCookie(t, r)
	if r.StatusCode != 200 || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.MaxAge <= 0 || strings.Contains(c.Value, tok) {
		t.Fatalf("%d %+v", r.StatusCode, c)
	}
	if r := doBody(t, "POST", ts.URL+"/auth/password/setup", origin, "", `{"password":"take it over"}`); r.StatusCode != 409 {
		t.Fatalf("second setup: %d", r.StatusCode)
	}
	if m := status(c.Value); m["configured"] != true || m["password"] != true || m["signed_in"] != true || m["setup_allowed"] != false {
		t.Fatalf("set up: %v", m)
	}

	// signed in: the page carries the API token; the API wants cookie and header
	page := doBody(t, "GET", ts.URL+"/", nil, c.Value, "")
	if b, _ := io.ReadAll(page.Body); !strings.Contains(string(b), `name="groundwork-token" content="`+tok+`"`) {
		t.Fatalf("signed-in page lacks the token: %s", b)
	}
	if r := doBody(t, "GET", ts.URL+"/api/workspace", map[string]string{TokenHeader: tok}, c.Value, ""); r.StatusCode != 200 {
		t.Fatalf("signed in: %d", r.StatusCode)
	}
	if r := doBody(t, "GET", ts.URL+"/api/workspace", nil, c.Value, ""); r.StatusCode != 401 {
		t.Fatalf("cookie without header: %d", r.StatusCode)
	}
	if r := doBody(t, "GET", ts.URL+"/api/events", nil, c.Value, ""); r.StatusCode == 401 {
		t.Fatal("the event stream takes the cookie alone")
	}

	// password sign-in from a fresh browser
	if r := doBody(t, "POST", ts.URL+"/auth/password/login", origin, "", `{"password":"wrong one"}`); r.StatusCode != 401 {
		t.Fatalf("wrong: %d", r.StatusCode)
	}
	c2 := sessionCookie(t, doBody(t, "POST", ts.URL+"/auth/password/login", origin, "", `{"password":"correct horse"}`))

	// account management, through the API
	api := map[string]string{"Origin": "http://" + host, TokenHeader: tok, "Content-Type": "application/json"}
	if r := doBody(t, "DELETE", ts.URL+"/api/auth/password", api, c.Value, ""); r.StatusCode != 409 {
		t.Fatalf("removing the only method: %d", r.StatusCode)
	}
	if r := doBody(t, "PUT", ts.URL+"/api/auth/password", api, c.Value, `{"current":"nope","password":"battery staple"}`); r.StatusCode != 401 {
		t.Fatalf("change without current: %d", r.StatusCode)
	}
	if r := doBody(t, "PUT", ts.URL+"/api/auth/password", api, c.Value, `{"current":"correct horse","password":"battery staple"}`); r.StatusCode != 200 {
		t.Fatalf("change: %d", r.StatusCode)
	}
	// signing the others out keeps this browser (a new cookie) and drops c2
	r = doBody(t, "POST", ts.URL+"/api/auth/sign-out-others", api, c.Value, "")
	c3 := sessionCookie(t, r)
	if status(c2.Value)["signed_in"] != false || status(c.Value)["signed_in"] != false || status(c3.Value)["signed_in"] != true {
		t.Fatal("sign-out-others")
	}
	// sign out clears the cookie
	if r := doBody(t, "POST", ts.URL+"/auth/sign-out", origin, c3.Value, ""); sessionCookie(t, r).MaxAge >= 0 {
		t.Fatal("sign-out should expire the cookie")
	}
	// `groundwork auth reset` deletes the file: everyone is signed out, setup is open again
	if err := auth.Reset(home); err != nil {
		t.Fatal(err)
	}
	if m := status(c3.Value); m["signed_in"] != false || m["configured"] != false {
		t.Fatalf("after reset: %v", m)
	}
}

func TestPasswordGuessingIsSlowedDown(t *testing.T) {
	ts, host, _ := newAuthServer(t)
	origin := map[string]string{"Origin": "http://" + host}
	doBody(t, "POST", ts.URL+"/auth/password/setup", origin, "", `{"password":"correct horse"}`)
	var last *http.Response
	for range 6 {
		last = doBody(t, "POST", ts.URL+"/auth/password/login", origin, "", `{"password":"guess"}`)
	}
	if last.StatusCode != 429 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("got %d", last.StatusCode)
	}
}

// First-run setup belongs to someone at this machine, not to whoever reaches
// a --host 0.0.0.0 service first.
func TestSetupOnlyFromLoopback(t *testing.T) {
	a, _ := auth.New(t.TempDir(), "")
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), events.NewBus(), Info{}, tok)
	s.SetAuth(a)
	s.AllowHost("10.0.0.5:7420")
	h := s.Handler()
	for _, p := range []string{"/auth/password/setup", "/auth/passkeys/register/begin"} {
		req := httptest.NewRequest("POST", "http://10.0.0.5:7420"+p, strings.NewReader(`{"password":"correct horse"}`))
		req.RemoteAddr = "10.0.0.9:5555"
		req.Header.Set("Origin", "http://10.0.0.5:7420")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Errorf("%s from the LAN: %d", p, w.Code)
		}
	}
	if a.Configured() {
		t.Fatal("a remote client set sign-in up")
	}
}

// Pages on 127.0.0.1 move to localhost, where passkeys and the cookie live;
// the API (the CLI talks to 127.0.0.1) does not.
func TestCanonicalHostRedirect(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), events.NewBus(), Info{}, tok)
	s.AllowPort(7420)
	s.SetCanonicalHost("localhost:7420")
	h := s.Handler()
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}
	if w := get("http://127.0.0.1:7420/p/x/runs?a=1"); w.Code != 307 || w.Header().Get("Location") != "http://localhost:7420/p/x/runs?a=1" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Location"))
	}
	if w := get("http://[::1]:7420/"); w.Code != 307 {
		t.Fatalf("[::1]: %d", w.Code)
	}
	if w := get("http://127.0.0.1:7420/api/workspace"); w.Code == 307 {
		t.Fatal("API requests are not redirected")
	}
	if w := get("http://localhost:7420/"); w.Code != 200 {
		t.Fatalf("localhost: %d", w.Code)
	}
}

func TestHostRebindingRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	r := do(t, "GET", ts.URL+"/healthz", map[string]string{"Host": "evil.example.com"}, true)
	if r.StatusCode != 403 {
		t.Fatalf("got %d", r.StatusCode)
	}
}

func TestOriginCheck(t *testing.T) {
	ts, host := newTestServer(t)
	h := map[string]string{TokenHeader: tok}
	if r := do(t, "POST", ts.URL+"/api/workspace", h, true); r.StatusCode != 403 {
		t.Errorf("no origin: %d", r.StatusCode)
	}
	h["Origin"] = "http://evil.example.com"
	if r := do(t, "POST", ts.URL+"/api/workspace", h, true); r.StatusCode != 403 {
		t.Errorf("evil origin: %d", r.StatusCode)
	}
	h["Origin"] = "http://" + host
	// passes origin check; route has no POST handler so chi answers 405
	if r := do(t, "POST", ts.URL+"/api/workspace", h, true); r.StatusCode == 403 {
		t.Errorf("good origin rejected")
	}
}

// Only event streams skip the token header (EventSource can't send it).
func TestEventStreamPaths(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/events":                true,
		"/api/p/infra-a1b2c3/events": true,
		"/api/p//events":             false,
		"/api/p/x/events/extra":      false,
		"/api/p/x/runs/1/events":     false,
		"/api/projects":              false,
		"/api/p/x/workspace":         false,
		"/api/eventsx":               false,
	} {
		if got := isEventStream(p); got != want {
			t.Errorf("isEventStream(%q) = %v, want %v", p, got, want)
		}
	}
}

// OpenStreams counts console event streams while they are connected: the
// service leaves desktop notifications to an open console.
func TestOpenStreamsCountsConnectedConsoles(t *testing.T) {
	bus := events.NewBus()
	ts := httptest.NewServer(SSE(bus))
	defer ts.Close()
	before := OpenStreams()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if n, _ := res.Body.Read(buf); !strings.Contains(string(buf[:n]), "hello") {
		t.Fatalf("%q", buf[:n])
	}
	if got := OpenStreams(); got != before+1 {
		t.Fatalf("open streams %d, want %d", got, before+1)
	}
	cancel()
	res.Body.Close()
	for i := 0; OpenStreams() != before; i++ {
		if i > 200 {
			t.Fatalf("stream still counted after disconnect: %d", OpenStreams())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
