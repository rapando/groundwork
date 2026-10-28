package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/v2/internal/server"
)

const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestProjectID(t *testing.T) {
	a, b := ProjectID("/src/team-a/Infra"), ProjectID("/src/team-b/Infra")
	if a == b {
		t.Fatalf("same-named folders collide: %s", a)
	}
	if !strings.HasPrefix(a, "infra-") || a != ProjectID("/src/team-a/Infra") {
		t.Fatalf("unstable or unexpected id %q", a)
	}
	if id := ProjectID("/x/!!!"); !strings.HasPrefix(id, "project-") {
		t.Fatalf("empty slug: %q", id)
	}
}

func TestRepoNameAndRemotes(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/Infra.git": "infra",
		"git@github.com:acme/platform.git":  "platform",
		"ssh://git@host:22/team/ops/":       "ops",
	} {
		if got := repoName(in); got != want {
			t.Errorf("repoName(%q) = %q, want %q", in, got, want)
		}
		if !remoteRe.MatchString(in) {
			t.Errorf("%q should be accepted", in)
		}
	}
	for _, bad := range []string{"ext::sh -c touch% /tmp/x", "file:///etc", "/local/path", "--upload-pack=x", "http://plain"} {
		if remoteRe.MatchString(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// projects.json carries a format number; a file from a newer release with a
// higher one is refused with a message rather than read wrongly.
func TestRegistryRefusesANewerFormat(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "projects.json"), []byte(`{"format": 99, "projects": []}`), 0o600)
	if _, err := OpenRegistry(home); err == nil || !strings.Contains(err.Error(), "newer groundwork") {
		t.Fatalf("got %v", err)
	}
	// a pre-0.5 file has no format at all and still loads
	os.WriteFile(filepath.Join(home, "projects.json"), []byte(`{"projects": []}`), 0o600)
	r, err := OpenRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Add(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "projects.json")); !strings.Contains(string(b), `"format": 1`) {
		t.Fatalf("saved without a format: %s", b)
	}
}

// A corrupt projects.json is set aside, not fatal: the service starts with
// no projects and the file is kept for the user to repair.
func TestRegistryQuarantinesACorruptFile(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "projects.json"), []byte(`{"projects": [`), 0o600)
	r, err := OpenRegistry(home)
	if err != nil || r.Quarantined == "" || len(r.List()) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	moved, _ := filepath.Glob(filepath.Join(home, "projects.json.broken-*"))
	if len(moved) != 1 {
		t.Fatalf("damaged file not set aside: %v", moved)
	}
	if _, err := os.Stat(filepath.Join(home, "projects.json")); err == nil {
		t.Fatal("the damaged file should have been moved")
	}
	if _, _, err := r.Add(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	if again, err := OpenRegistry(home); err != nil || again.Quarantined != "" || len(again.List()) != 1 {
		t.Fatalf("fresh file: %v %+v", err, again)
	}
}

func TestRegistryPersists(t *testing.T) {
	home := t.TempDir()
	r, err := OpenRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	p, added, err := r.Add("/repos/infra", "")
	if err != nil || !added {
		t.Fatalf("add: %v %v", added, err)
	}
	if _, again, _ := r.Add("/repos/infra", ""); again {
		t.Fatal("same path added twice")
	}
	r2, err := OpenRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r2.Get(p.ID); !ok || got.Path != "/repos/infra" {
		t.Fatalf("not persisted: %+v", got)
	}
	if _, ok, _ := r2.Remove(p.ID); !ok || len(r2.List()) != 0 {
		t.Fatal("remove failed")
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := exec.Command("cp", "-R", "../../testdata/repos/iac-only/.", dir).Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Fatal(err)
	}
	return dir
}

type env struct {
	svc  *Service
	base string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	svc, err := New(ctx, t.TempDir(), "test", quiet())
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(quiet(), nil, server.Info{Version: "test"}, tok)
	srv.SetAPI(svc.Routes)
	ts := httptest.NewServer(srv.Handler())
	srv.AllowHost(strings.TrimPrefix(ts.URL, "http://"))
	t.Cleanup(func() {
		ts.Close()
		svc.Shutdown(context.Background())
		cancel()
	})
	return &env{svc, ts.URL}
}

func (e *env) call(t *testing.T, method, path string, body any, auth bool) (*http.Response, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	req.Header.Set("Origin", e.base)
	if auth {
		req.Header.Set(server.TokenHeader, tok)
		req.AddCookie(&http.Cookie{Name: "groundwork_session", Value: tok})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp, out
}

func TestProjectsAreServedUnderTheirOwnPrefix(t *testing.T) {
	e := newEnv(t)
	dir := repo(t)
	sub := filepath.Join(dir, "terraform")
	_ = os.MkdirAll(sub, 0o755)

	// importing a subfolder imports the repository root
	resp, p := e.call(t, "POST", "/api/projects", map[string]string{"path": sub}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("add: %d %v", resp.StatusCode, p)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if p["path"] != real {
		t.Fatalf("path = %v, want repo root %s", p["path"], real)
	}
	id := p["id"].(string)
	if resp, _ := e.call(t, "POST", "/api/projects", map[string]string{"path": dir}, true); resp.StatusCode != 200 {
		t.Fatalf("re-adding should return the existing project, got %d", resp.StatusCode)
	}

	resp, ws := e.call(t, "GET", "/api/p/"+id+"/workspace", nil, true)
	if resp.StatusCode != 200 || ws["root"] != real {
		t.Fatalf("workspace: %d %v", resp.StatusCode, ws)
	}
	if resp, _ := e.call(t, "GET", "/api/p/"+id+"/workspace", nil, false); resp.StatusCode != 401 {
		t.Fatalf("project API without credentials: %d", resp.StatusCode)
	}
	if resp, _ := e.call(t, "GET", "/api/p/nope-000000/workspace", nil, true); resp.StatusCode != 404 {
		t.Fatalf("unknown project: %d", resp.StatusCode)
	}
	if resp, _ := e.call(t, "GET", "/api/p/"+id+"/no-such-endpoint", nil, true); resp.StatusCode != 404 {
		t.Fatalf("unknown endpoint: %d", resp.StatusCode)
	}

	if resp, _ := e.call(t, "DELETE", "/api/projects/"+id, nil, true); resp.StatusCode != 200 {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	if resp, _ := e.call(t, "GET", "/api/p/"+id+"/workspace", nil, true); resp.StatusCode != 404 {
		t.Fatalf("removed project still served: %d", resp.StatusCode)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("remove must leave files in place")
	}
}

func TestMissingFolderIsReportedNotFatal(t *testing.T) {
	e := newEnv(t)
	dir := repo(t)
	_, p := e.call(t, "POST", "/api/projects", map[string]string{"path": dir}, true)
	id := p["id"].(string)

	// a second service over the same home, after the folder disappeared
	e.svc.Shutdown(context.Background())
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	svc, err := New(context.Background(), e.svc.Home, "test", quiet())
	if err != nil {
		t.Fatal(err)
	}
	svc.OpenAll()
	defer svc.Shutdown(context.Background())
	views := svc.views()
	if len(views) != 1 || views[0].ID != id || views[0].Status != "unavailable" || views[0].Error == "" {
		t.Fatalf("views = %+v", views)
	}
}

func TestAddRefusesBroadOrMissingFolders(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"", "/definitely/not/here", "/", ".", "arch"} {
		if resp, _ := e.call(t, "POST", "/api/projects", map[string]string{"path": p}, true); resp.StatusCode != 422 {
			t.Errorf("%q: got %d, want 422", p, resp.StatusCode)
		}
	}
	if resp, _ := e.call(t, "POST", "/api/projects", map[string]string{"url": "ext::sh -c id"}, true); resp.StatusCode != 422 {
		t.Errorf("ext:: URL: got %d, want 422", resp.StatusCode)
	}
}

func TestCloneImportsTheCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	e := newEnv(t)
	src := repo(t)
	g := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", src, "-c", "user.name=t", "-c", "user.email=t@e.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	g("add", "-A")
	g("commit", "-qm", "init")
	// Clone only accepts network URLs; clone the local repo under one
	url := "git@example.com:acme/infra.git"
	p, added, err := e.svc.cloneFrom(context.Background(), url, src)
	if err != nil || !added {
		t.Fatalf("clone: %v %v", added, err)
	}
	if p.Remote != url || !strings.HasPrefix(p.Path, mustReal(t, e.svc.reposDir())) {
		t.Fatalf("project = %+v", p)
	}
	if _, err := os.Stat(filepath.Join(p.Path, ".git")); err != nil {
		t.Fatal("not a checkout")
	}
	if again, added, _ := e.svc.cloneFrom(context.Background(), url, src); added || again.ID != p.ID {
		t.Fatal("same URL cloned twice")
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	_ = os.MkdirAll(p, 0o755)
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClientResolvesRelativePathsFromTheCaller(t *testing.T) {
	e := newEnv(t)
	dir := repo(t)
	sub := filepath.Join(dir, "terraform")
	_ = os.MkdirAll(sub, 0o755)
	t.Chdir(sub)

	port, _ := strconv.Atoi(e.base[strings.LastIndex(e.base, ":")+1:])
	c := NewClient(ServerInfo{Port: port, Token: tok})
	for _, rel := range []string{".", "./", "../terraform"} {
		p, err := c.AddPath(rel)
		if err != nil {
			t.Fatalf("%q: %v", rel, err)
		}
		real, _ := filepath.EvalSymlinks(dir)
		if p.Path != real {
			t.Fatalf("%q: path = %s, want %s", rel, p.Path, real)
		}
	}
}

// GET /api/service describes the running service for `service status`.
func TestServiceStatusEndpoint(t *testing.T) {
	e := newEnv(t)
	e.svc.Commit, e.svc.Started, e.svc.Port = "abc1234", time.Now().Add(-90*time.Second), 7420
	dir := repo(t)
	if resp, p := e.call(t, "POST", "/api/projects", map[string]string{"path": dir}, true); resp.StatusCode != 201 {
		t.Fatalf("add: %d %v", resp.StatusCode, p)
	}
	resp, st := e.call(t, "GET", "/api/service", nil, true)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %v", resp.StatusCode, st)
	}
	if st["version"] != "test" || st["commit"] != "abc1234" || st["port"].(float64) != 7420 || st["pid"].(float64) <= 0 ||
		!strings.HasPrefix(st["go_version"].(string), "go") || st["uptime_seconds"].(float64) < 90 {
		t.Fatalf("status: %v", st)
	}
	if pr := st["projects"].(map[string]any); pr["available"].(float64) != 1 || pr["unavailable"].(float64) != 0 {
		t.Fatalf("projects: %v", pr)
	}
	if st["running_jobs"].(float64) != 0 {
		t.Fatalf("running jobs: %v", st["running_jobs"])
	}
	if resp, _ := e.call(t, "GET", "/api/service", nil, false); resp.StatusCode != 401 {
		t.Fatalf("status without credentials: %d", resp.StatusCode)
	}
}

// A project whose state.db can't be opened keeps the VERSION of the release
// that last opened it, so a downgrade doesn't erase the evidence.
func TestRefusedStoreKeepsVersionMarker(t *testing.T) {
	e := newEnv(t)
	dir := repo(t)
	e.call(t, "POST", "/api/projects", map[string]string{"path": dir}, true)
	e.svc.Shutdown(context.Background())

	state := filepath.Join(dir, ".groundwork")
	if err := os.WriteFile(filepath.Join(state, "VERSION"), []byte("9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := New(context.Background(), e.svc.Home, "test", quiet())
	if err != nil {
		t.Fatal(err)
	}
	svc.OpenAll()
	defer svc.Shutdown(context.Background())
	if v := svc.views(); len(v) != 1 || v[0].Status != "unavailable" {
		t.Fatalf("views = %+v", v)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "VERSION")); string(b) != "9.9.9\n" {
		t.Fatalf("VERSION overwritten: %q", b)
	}
}
