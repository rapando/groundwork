package server_test

// Security suite: the whole HTTP stack (server middleware + every API route)
// against the threats in the plan: drive-by requests from other sites, DNS
// rebinding, missing credentials, path escapes, argument injection, caching
// and framing.

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/api"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/server"
	"github.com/rapando/groundwork/internal/store"
)

const token = "0123456789abcdef0123456789abcdef"

type stack struct {
	url, host, root, outside string
	routes                   [][2]string // method, pattern
	t                        *testing.T
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newStack(t *testing.T) *stack {
	t.Helper()
	root := t.TempDir()
	copyTree(t, "../../testdata/repos/iac-only", root)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside-the-repo"), 0o644)
	os.WriteFile(filepath.Join(outside, "x.yml"), []byte("password: hunter2-outside\n"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := api.New(root, "test", events.NewBus(), st)
	s := server.New(slog.New(slog.NewTextHandler(io.Discard, nil)), events.NewBus(), server.Info{Root: root, Version: "t"}, token)
	s.SetAPI(a.Routes)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	host := strings.TrimPrefix(ts.URL, "http://")
	s.AllowHost(host)

	// every route the API mounts
	r := chi.NewRouter()
	a.Routes(r)
	var routes [][2]string
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, [2]string{method, route})
		return nil
	})
	sk := &stack{ts.URL, host, root, outside, routes, t}
	if res := sk.call("POST", "/api/setup/apply", map[string]any{}, true); res.StatusCode != 200 {
		t.Fatalf("setup: %d", res.StatusCode)
	}
	return sk
}

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

func (s *stack) req(method, path string, body any, mod func(*http.Request)) *http.Response {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.url+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if mod != nil {
		mod(req)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { res.Body.Close() })
	return res
}

// call sends an authenticated same-origin request.
func (s *stack) call(method, path string, body any, ok bool) *http.Response {
	return s.req(method, path, body, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "groundwork_session", Value: token})
		r.Header.Set(server.TokenHeader, token)
		r.Header.Set("Origin", s.url)
	})
}

func TestEveryAPIRouteRequiresSessionTokenAndOrigin(t *testing.T) {
	s := newStack(t)
	if len(s.routes) < 50 {
		t.Fatalf("only %d routes found", len(s.routes))
	}
	for _, rt := range s.routes {
		method, path := rt[0], "/api"+paramRe.ReplaceAllString(rt[1], "1")
		t.Run(method+" "+path, func(t *testing.T) {
			// no cookie, no token
			if res := s.req(method, path, map[string]any{}, func(r *http.Request) { r.Header.Set("Origin", s.url) }); res.StatusCode != 401 {
				t.Errorf("no credentials: %d", res.StatusCode)
			}
			// cookie but no token header (a page that got the cookie sent along)
			if res := s.req(method, path, map[string]any{}, func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "groundwork_session", Value: token})
				r.Header.Set("Origin", s.url)
			}); res.StatusCode != 401 {
				t.Errorf("cookie only: %d", res.StatusCode)
			}
			// wrong token
			if res := s.req(method, path, map[string]any{}, func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "groundwork_session", Value: token})
				r.Header.Set(server.TokenHeader, strings.Repeat("0", len(token)))
				r.Header.Set("Origin", s.url)
			}); res.StatusCode != 401 {
				t.Errorf("wrong token: %d", res.StatusCode)
			}
			// DNS rebinding: the attacker's hostname resolving to 127.0.0.1
			if res := s.req(method, path, map[string]any{}, func(r *http.Request) {
				r.Host = "evil.example:" + strings.Split(s.host, ":")[1]
				r.AddCookie(&http.Cookie{Name: "groundwork_session", Value: token})
				r.Header.Set(server.TokenHeader, token)
			}); res.StatusCode != 403 {
				t.Errorf("rebinding: %d", res.StatusCode)
			}
			if method == "GET" || method == "HEAD" {
				return
			}
			// CSRF: full credentials, foreign origin (or none at all)
			for _, origin := range []string{"http://evil.example", "null", ""} {
				if res := s.req(method, path, map[string]any{}, func(r *http.Request) {
					r.AddCookie(&http.Cookie{Name: "groundwork_session", Value: token})
					r.Header.Set(server.TokenHeader, token)
					if origin != "" {
						r.Header.Set("Origin", origin)
					}
				}); res.StatusCode != 403 {
					t.Errorf("origin %q: %d", origin, res.StatusCode)
				}
			}
		})
	}
}

func TestHeadersOnEveryKindOfResponse(t *testing.T) {
	s := newStack(t)
	cases := map[string]*http.Response{
		"page":      s.req("GET", "/", nil, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "groundwork_session", Value: token}) }),
		"api":       s.call("GET", "/api/workspace", nil, true),
		"api error": s.call("GET", "/api/runs/999999", nil, true),
		"401":       s.req("GET", "/api/workspace", nil, nil),
		"bad host":  s.req("GET", "/", nil, func(r *http.Request) { r.Host = "evil.example" }),
	}
	for name, res := range cases {
		h := res.Header
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: missing headers %v", name, h)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "script-src 'self';") {
			t.Errorf("%s: csp %q", name, csp)
		}
	}
	if cases["api"].Header.Get("Cache-Control") != "no-store" || cases["page"].Header.Get("Cache-Control") != "no-store" {
		t.Error("API responses and the page (it holds the token) must not be cached")
	}
	// the token exchange strips ?t= from the URL it redirects to
	res := s.req("GET", "/?t="+token+"&x=1", nil, nil)
	if res.StatusCode != 303 || strings.Contains(res.Header.Get("Location"), token) {
		t.Fatalf("%d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if ck := res.Header.Get("Set-Cookie"); !strings.Contains(ck, "HttpOnly") || !strings.Contains(ck, "SameSite=Strict") {
		t.Errorf("cookie flags: %s", ck)
	}
}

// Every endpoint that takes a path, a root or an argument refuses to step
// outside the repository or smuggle options.
func TestNoPathEscapesOrArgumentInjection(t *testing.T) {
	s := newStack(t)
	escapes := []string{"../../etc/passwd", "/etc/passwd", "escape/secret.txt", "escape/x.yml", "terraform/../../outside"}
	protected := []string{".git/config", ".groundwork/state.db", "terraform/envs/dev/terraform.tfstate"}
	type ep struct {
		method, path string
		body         any
		content      bool // reads or writes file content (so protected paths are refused too)
	}
	endpoints := func(p string) []ep {
		q := strings.ReplaceAll(p, "%", "%25")
		return []ep{
			{"GET", "/api/files/content?path=" + q, nil, true},
			{"PUT", "/api/files/content?path=" + q, map[string]any{"content": "pwned", "create": true}, true},
			{"GET", "/api/git/diff?path=" + q, nil, false},
			{"GET", "/api/graph/roots?file=" + q, nil, false},
			{"POST", "/api/checks/fix", map[string]any{"kind": "fmt", "file": p}, true},
			{"PUT", "/api/vars/terraform", map[string]any{"root": "terraform/envs/dev", "env": "dev", "name": "region", "value": `"x"`, "file": p, "apply": true}, true},
			{"POST", "/api/secrets/move", map[string]any{"file": p, "line": 1, "apply": true}, true},
		}
	}
	check := func(p string, mustReject func(ep) bool) {
		for _, c := range endpoints(p) {
			res := s.call(c.method, c.path, c.body, true)
			b, _ := io.ReadAll(res.Body)
			if mustReject(c) && res.StatusCode < 400 {
				t.Errorf("%s %s: %d %s", c.method, c.path, res.StatusCode, b)
			}
			if bytes.Contains(b, []byte("outside-the-repo")) || bytes.Contains(b, []byte("hunter2-outside")) || bytes.Contains(b, []byte("root:")) || bytes.Contains(b, []byte("[core]")) {
				t.Errorf("%s %s leaked content: %s", c.method, c.path, b)
			}
		}
	}
	for _, p := range escapes {
		check(p, func(ep) bool { return true })
	}
	for _, p := range protected {
		check(p, func(c ep) bool { return c.content })
	}
	// URL-encoded dots are literal names after one decoding: whatever happens stays inside
	check("%2e%2e/%2e%2e/etc/passwd", func(ep) bool { return false })
	if _, err := os.Stat(filepath.Join(s.root, "%2e%2e", "%2e%2e", "etc", "passwd")); err != nil {
		t.Logf("literal %%2e%%2e path not created (also fine): %v", err)
	}

	// nothing outside was modified or created
	if b, _ := os.ReadFile(filepath.Join(s.outside, "secret.txt")); string(b) != "outside-the-repo" {
		t.Fatal("a file outside the repository was modified")
	}
	if b, _ := os.ReadFile(filepath.Join(s.outside, "x.yml")); string(b) != "password: hunter2-outside\n" {
		t.Fatal("a file outside the repository was modified")
	}

	runs := []map[string]any{
		{"kind": "tf.plan", "root": "../../etc", "env": "dev"},
		{"kind": "tf.plan", "root": "/etc", "env": "dev"},
		{"kind": "tf.plan", "root": "terraform/envs/dev", "env": "-chdir=/"},
		{"kind": "tf.unlock", "root": "terraform/envs/dev", "env": "dev", "lock_id": "-force", "confirm_text": "dev"},
		{"kind": "tf.unlock", "root": "terraform/envs/dev", "env": "dev", "lock_id": "abc; rm -rf /", "confirm_text": "dev"},
		{"kind": "tf.unlock", "root": "terraform/envs/dev", "env": "dev", "lock_id": "6f1c2e0a-91b4-7d3e-a5c8-04e7b2f19d61"}, // no confirmation
		{"kind": "ans.check", "project": "../..", "env": "dev", "playbook": "site.yml"},
		{"kind": "ans.check", "project": "ansible", "env": "dev", "playbook": "../../etc/passwd"},
		{"kind": "ans.check", "project": "ansible", "env": "dev", "playbook": "/etc/passwd"},
		{"kind": "ans.check", "project": "ansible", "env": "dev", "playbook": "playbooks/site.yml", "limit": "-e@/etc/passwd"},
		{"kind": "ans.check", "project": "ansible", "env": "dev", "playbook": "playbooks/site.yml", "tags": "x --vault-password-file=/tmp/x"},
		{"kind": "ans.adhoc", "project": "ansible", "env": "dev", "pattern": "--help", "module": "ping"},
		{"kind": "ans.adhoc", "project": "ansible", "env": "dev", "pattern": "all", "module": "shell", "args": "id"}, // mutating, no confirmation
		{"kind": "ans.adhoc", "project": "ansible", "env": "dev", "pattern": "all", "module": "-a", "args": "id"},
		{"kind": "tf.destroy", "root": "terraform/envs/dev", "env": "dev"},
	}
	for _, body := range runs {
		if res := s.call("POST", "/api/runs", body, true); res.StatusCode < 400 {
			b, _ := io.ReadAll(res.Body)
			t.Errorf("%v accepted: %d %s", body, res.StatusCode, b)
		}
	}
	for _, q := range []string{"project=../..&env=dev", "project=ansible&env=../../etc", "project=/etc&env=dev"} {
		if res := s.call("GET", "/api/inventory?"+q, nil, true); res.StatusCode < 400 {
			b, _ := io.ReadAll(res.Body)
			if !bytes.Contains(b, []byte(`"error"`)) {
				t.Errorf("inventory %s: %d %s", q, res.StatusCode, b)
			}
		}
	}
}

// Request bodies are bounded, so a local page can't make the server buffer gigabytes.
func TestBodiesAreBounded(t *testing.T) {
	s := newStack(t)
	big := map[string]any{"content": strings.Repeat("x", 3<<20), "create": true}
	if res := s.call("PUT", "/api/files/content?path=big.tf", big, true); res.StatusCode < 400 {
		t.Fatalf("3 MiB write accepted: %d", res.StatusCode)
	}
	if res := s.call("POST", "/api/runs", map[string]any{"kind": strings.Repeat("a", 2<<20)}, true); res.StatusCode != 400 {
		t.Fatalf("2 MiB JSON: %d", res.StatusCode)
	}
}
