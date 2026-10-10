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

	"github.com/rapando/groundwork/internal/server"
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
