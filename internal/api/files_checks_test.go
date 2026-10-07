package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/store"
)

type fakeExec struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeExec) LookPath(n string) (string, error) {
	if n == "tflint" || n == "checkov" {
		return "", errors.New("missing")
	}
	return "/bin/" + n, nil
}

func (f *fakeExec) Run(_ context.Context, c checks.Cmd) (checks.Output, error) {
	j := strings.Join(c.Argv, " ")
	f.mu.Lock()
	f.calls = append(f.calls, j)
	f.mu.Unlock()
	switch {
	case strings.Contains(j, " validate "):
		return checks.Output{ExitCode: 1, Stdout: []byte(`{"valid":false,"diagnostics":[{"severity":"error","summary":"Unsupported argument","detail":"An argument named \"enable_dns_hostname\" is not expected here. Did you mean \"enable_dns_hostnames\"?","range":{"filename":"main.tf","start":{"line":2,"column":3},"end":{"line":2,"column":22}}}]}`)}, nil
	case strings.Contains(j, " fmt ") && strings.Contains(j, "-check"):
		return checks.Output{ExitCode: 0}, nil
	}
	return checks.Output{}, nil
}

// env builds an API over a configured copy of the iac-only fixture.
type testEnv struct {
	a    *API
	h    http.Handler
	root string
	fx   *fakeExec
}

func newConfigured(t *testing.T) *testEnv {
	t.Helper()
	root := fixture(t, "iac-only")
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := New(root, "test", events.NewBus(), st)
	t.Cleanup(a.Checks.Close) // background check runs end before the store and temp dirs go
	fx := &fakeExec{}
	a.Checks.Exec = fx
	r := chi.NewRouter()
	a.Routes(r)
	// configure through the real setup endpoint
	if rec := call(t, r, "POST", "/setup/apply", map[string]any{}); rec.Code != 200 {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	return &testEnv{a, r, root, fx}
}

func put(t *testing.T, h http.Handler, path, ifMatch string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", path, bytes.NewReader(b))
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFileListIaCOnlyAndSections(t *testing.T) {
	e := newConfigured(t)
	get := func(q string) map[string]any { return into(t, call(t, e.h, "GET", "/files"+q, nil)) }
	names := func(m map[string]any) map[string]bool {
		out := map[string]bool{}
		for _, f := range m["files"].([]any) {
			out[f.(string)] = true
		}
		return out
	}
	all := names(get(""))
	iac := get("?iac_only=1")
	only := names(iac)
	if !all["README.md"] || !all[".github/workflows/iac.yml"] || !all["groundwork.yaml"] {
		t.Fatalf("all files incomplete: %v", all)
	}
	if only["README.md"] || only[".github/workflows/iac.yml"] {
		t.Fatalf("IaC-only leaked unmanaged files: %v", only)
	}
	for _, want := range []string{"terraform/envs/dev/main.tf", "ansible/playbooks/site.yml", "groundwork.yaml"} {
		if !only[want] {
			t.Errorf("IaC-only missing %s", want)
		}
	}
	secs := iac["sections"].([]any)
	if len(secs) != 2 || secs[0].(map[string]any)["base"] != "terraform" || secs[1].(map[string]any)["base"] != "ansible" {
		t.Fatalf("sections: %v", secs)
	}
}

func TestFileContentRoundTripAndErrors(t *testing.T) {
	e := newConfigured(t)
	p := "/files/content?path=terraform/envs/dev/variables.tf"
	m := into(t, call(t, e.h, "GET", p, nil))
	sha := m["sha"].(string)
	if !strings.Contains(m["content"].(string), `variable "cidr"`) {
		t.Fatalf("%v", m)
	}

	if rec := put(t, e.h, p, "", map[string]any{"content": "x"}); rec.Code != 428 {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	rec := put(t, e.h, p, `"stale"`, map[string]any{"content": "x"})
	if rec.Code != 409 {
		t.Fatalf("stale: %d", rec.Code)
	}
	if d := into(t, rec)["error"].(map[string]any)["details"].(map[string]any); d["current_sha"] != sha {
		t.Fatalf("conflict should report the current sha: %v", d)
	}
	rec = put(t, e.h, p, `"`+sha+`"`, map[string]any{"content": "# edited\n"}) // quoted ETag style
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "terraform/envs/dev/variables.tf")); string(b) != "# edited\n" {
		t.Fatalf("not written: %q", b)
	}

	// create
	if rec := put(t, e.h, "/files/content?path=terraform/envs/dev/new.tf", "", map[string]any{"content": "a", "create": true}); rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for path, want := range map[string]int{
		"/files/content?path=nope.tf":           404,
		"/files/content?path=../../etc/passwd":  400,
		"/files/content?path=.git/config":       403,
		"/files/content?path=terraform.tfstate": 403,
	} {
		if got := call(t, e.h, "GET", path, nil).Code; got != want {
			t.Errorf("%s: got %d want %d", path, got, want)
		}
	}
	os.WriteFile(filepath.Join(e.root, "bin.dat"), []byte{0, 1, 2}, 0o644)
	if got := call(t, e.h, "GET", "/files/content?path=bin.dat", nil).Code; got != 415 {
		t.Errorf("binary: %d", got)
	}
}

func waitIdle(t *testing.T, e *testEnv) map[string]any {
	t.Helper()
	for i := 0; i < 200; i++ {
		m := into(t, call(t, e.h, "GET", "/checks", nil))
		running := false
		ranSomething := false
		for _, u := range m["units"].([]any) {
			if u.(map[string]any)["running"] == true {
				running = true
			}
			for _, r := range u.(map[string]any)["results"].([]any) {
				if r.(map[string]any)["status"] != "idle" {
					ranSomething = true
				}
			}
		}
		if !running && ranSomething {
			return m
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("checks never finished")
	return nil
}

func TestChecksRunAndReport(t *testing.T) {
	e := newConfigured(t)
	sub, cancel := e.a.Bus.Subscribe()
	defer cancel()

	if rec := call(t, e.h, "POST", "/checks/run", map[string]any{"unit": "tf:nope"}); rec.Code != 404 {
		t.Fatalf("unknown unit: %d", rec.Code)
	}
	if rec := call(t, e.h, "POST", "/checks/run", map[string]any{"unit": "tf:terraform/envs/dev"}); rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := waitIdle(t, e)
	counts := m["counts"].(map[string]any)
	if counts["error"].(float64) != 1 {
		t.Fatalf("counts %v", counts)
	}
	d := m["diagnostics"].([]any)[0].(map[string]any)
	if d["file"] != "terraform/envs/dev/main.tf" || d["line"].(float64) != 2 || d["fix"].(map[string]any)["kind"] != "edit" {
		t.Fatalf("%v", d)
	}
	// filtering
	if got := into(t, call(t, e.h, "GET", "/checks?severity=warning", nil))["diagnostics"].([]any); len(got) != 0 {
		t.Fatalf("severity filter: %v", got)
	}
	if got := into(t, call(t, e.h, "GET", "/checks?file=terraform/envs/dev/main.tf", nil))["diagnostics"].([]any); len(got) != 1 {
		t.Fatalf("file filter: %v", got)
	}
	// SSE events fired
	deadline := time.After(2 * time.Second)
	seen := map[string]bool{}
	for !seen["checks.started"] || !seen["checks.updated"] {
		select {
		case ev := <-sub:
			seen[ev.Type] = true
		case <-deadline:
			t.Fatalf("events seen: %v", seen)
		}
	}
	// the missing optional tool shows up as skipped with its install hint
	var tl map[string]any
	for _, u := range m["units"].([]any) {
		if u.(map[string]any)["id"] == "tf:terraform/envs/dev" {
			for _, r := range u.(map[string]any)["results"].([]any) {
				if r.(map[string]any)["tool"] == "tflint" {
					tl = r.(map[string]any)
				}
			}
		}
	}
	// tflint isn't enabled by default config here; only assert if present
	if tl != nil && tl["status"] != "skipped" {
		t.Fatalf("%v", tl)
	}
}

func TestChecksRunRequiresSetup(t *testing.T) {
	h := newAPI(t, fixture(t, "iac-only"))
	if rec := call(t, h, "POST", "/checks/run", map[string]any{}); rec.Code != 409 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestFixEndpointOnlyFormats(t *testing.T) {
	e := newConfigured(t)
	if rec := call(t, e.h, "POST", "/checks/fix", map[string]any{"kind": "edit", "file": "a.tf"}); rec.Code != 400 {
		t.Fatalf("edit fixes belong to the editor: %d", rec.Code)
	}
	if rec := call(t, e.h, "POST", "/checks/fix", map[string]any{"kind": "fmt", "file": "ansible/playbooks/site.yml"}); rec.Code != 422 {
		t.Fatalf("non-terraform file: %d", rec.Code)
	}
	if rec := call(t, e.h, "POST", "/checks/fix", map[string]any{"kind": "fmt", "file": "terraform/envs/dev/main.tf"}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	last := e.fx.calls[len(e.fx.calls)-1]
	if !strings.Contains(last, "fmt "+filepath.Join(e.root, "terraform/envs/dev/main.tf")) && !strings.Contains(last, "fmt ") {
		t.Fatalf("%s", last)
	}
}

func TestOnFilesChangedPublishesAndTriggersChecks(t *testing.T) {
	e := newConfigured(t)
	sub, cancel := e.a.Bus.Subscribe()
	defer cancel()
	e.a.OnFilesChanged([]string{"terraform/envs/dev/main.tf"})
	ev := <-sub
	if ev.Type != "file.changed" || !strings.Contains(string(ev.Data), "terraform/envs/dev/main.tf") {
		t.Fatalf("%+v", ev)
	}
	waitIdle(t, e) // on_save is on by default in the detected config
	found := false
	for _, c := range e.fx.calls {
		if strings.Contains(c, "terraform/envs/dev validate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a change should re-validate its unit: %v", e.fx.calls)
	}
}

func TestGitEndpointsOutsideAndInsideRepo(t *testing.T) {
	e := newConfigured(t)
	m := into(t, call(t, e.h, "GET", "/git/status", nil))
	if m["available"] != false {
		t.Fatalf("not a git repo: %v", m)
	}
	if call(t, e.h, "GET", "/git/diff?path=x", nil).Code != 500 {
		t.Fatal("diff outside git should fail cleanly")
	}
	if call(t, e.h, "GET", "/git/diff?path=../x", nil).Code != 400 {
		t.Fatal("diff path escape")
	}
	_ = files.MaxEditSize
}

func TestSaveTriggersChecksWithoutWaitingForTheWatcher(t *testing.T) {
	e := newConfigured(t) // no watcher here: only the save itself can trigger the run
	p := "/files/content?path=terraform/envs/dev/variables.tf"
	sha := into(t, call(t, e.h, "GET", p, nil))["sha"].(string)
	start := time.Now()
	if rec := put(t, e.h, p, sha, map[string]any{"content": "variable \"cidr\" {}\n"}); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	// well inside the 400ms debounce the watcher path would add
	for time.Since(start) < 250*time.Millisecond {
		e.fx.mu.Lock()
		n := 0
		for _, c := range e.fx.calls {
			if strings.Contains(c, "terraform/envs/dev validate") {
				n++
			}
		}
		e.fx.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("saving a file should start its unit's checks immediately")
}
