package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
)

const tok = "secret-token"

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), events.NewBus(), Info{Root: "/r", Version: "t"}, tok)
	s.SetAPI(func(r chi.Router) {
		r.Get("/workspace", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.info) })
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	s.AllowHost(strings.TrimPrefix(ts.URL, "http://"))
	return ts, strings.TrimPrefix(ts.URL, "http://")
}

func do(t *testing.T, method, url string, hdr map[string]string, cookie bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	if cookie {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRejectsWithoutToken(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, p := range []string{"/", "/api/workspace", "/api/events"} {
		if r := do(t, "GET", ts.URL+p, nil, false); r.StatusCode != 401 {
			t.Errorf("%s: got %d want 401", p, r.StatusCode)
		}
	}
	// cookie but no header on a normal API route
	if r := do(t, "GET", ts.URL+"/api/workspace", nil, true); r.StatusCode != 401 {
		t.Errorf("missing header: got %d", r.StatusCode)
	}
	// header but no cookie
	if r := do(t, "GET", ts.URL+"/api/workspace", map[string]string{TokenHeader: tok}, false); r.StatusCode != 401 {
		t.Errorf("missing cookie: got %d", r.StatusCode)
	}
}

func TestAcceptsValidSession(t *testing.T) {
	ts, _ := newTestServer(t)
	if r := do(t, "GET", ts.URL+"/api/workspace", map[string]string{TokenHeader: tok}, true); r.StatusCode != 200 {
		t.Fatalf("got %d", r.StatusCode)
	}
}

func TestTokenExchange(t *testing.T) {
	ts, _ := newTestServer(t)
	r := do(t, "GET", ts.URL+"/?t="+tok, nil, false)
	if r.StatusCode != 303 || r.Header.Get("Location") != "/" {
		t.Fatalf("got %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	var c *http.Cookie
	for _, k := range r.Cookies() {
		if k.Name == cookieName {
			c = k
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("bad cookie %+v", c)
	}
	if r := do(t, "GET", ts.URL+"/?t=wrong", nil, false); r.StatusCode != 401 {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
}

func TestIndexInjectsMeta(t *testing.T) {
	ts, _ := newTestServer(t)
	r := do(t, "GET", ts.URL+"/some/spa/route", nil, true)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !strings.Contains(string(b), `name="groundwork-token" content="`+tok+`"`) {
		t.Fatalf("status %d body %s", r.StatusCode, b)
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
