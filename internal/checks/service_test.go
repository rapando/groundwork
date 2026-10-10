package checks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/workspace"
)

type fakeExec struct {
	mu      sync.Mutex
	calls   []Cmd
	missing map[string]bool
	handler func(c Cmd) Output
}

func (f *fakeExec) LookPath(n string) (string, error) {
	if f.missing[n] {
		return "", errors.New("not found")
	}
	return "/usr/bin/" + n, nil
}

func (f *fakeExec) Run(_ context.Context, c Cmd) (Output, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.handler != nil {
		return f.handler(c), nil
	}
	return Output{}, nil
}

func (f *fakeExec) count(sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.Argv, " "), sub) {
			n++
		}
	}
	return n
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
}

type env struct {
	svc  *Service
	fx   *fakeExec
	root string
	st   *store.Store
	sub  <-chan events.Event
}

func newEnv(t *testing.T, fx *fakeExec) *env {
	t.Helper()
	root := t.TempDir()
	copyTree(t, "../../testdata/repos/iac-only", root)
	rep, err := workspace.Detect(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.FromReport(rep)
	cfg.Checks.Enabled = []string{"fmt", "validate", "tflint", "ansible-lint", "yamllint", "syntax-check"}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus := events.NewBus()
	sub, cancel := bus.Subscribe()
	t.Cleanup(cancel)
	svc := New(root, func() *config.Config { return cfg }, fx, st, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.debounce = 30 * time.Millisecond
	t.Cleanup(svc.Close) // before the store closes (cleanups run last-first)
	return &env{svc, fx, root, st, sub}
}

func gold(name string) string {
	b, err := os.ReadFile("../../testdata/checks/" + name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func realisticExec() *fakeExec {
	fx := &fakeExec{}
	fx.handler = func(c Cmd) Output {
		j := strings.Join(c.Argv, " ")
		switch {
		case strings.Contains(j, " validate "):
			if strings.Contains(j, "envs/dev") {
				return Output{Stdout: []byte(gold("validate_errors.json")), ExitCode: 1}
			}
			return Output{Stdout: []byte(`{"valid":true,"error_count":0,"warning_count":0,"diagnostics":[]}`)}
		case strings.Contains(j, " fmt "):
			if strings.Contains(j, "envs/dev") {
				return Output{Stdout: []byte("main.tf\n"), ExitCode: 3}
			}
		case strings.HasPrefix(j, "tflint"):
			return Output{Stdout: []byte(`{"issues":[],"errors":[]}`)}
		case strings.HasPrefix(j, "yamllint"):
			return Output{Stdout: []byte("roles/nginx/tasks/main.yml:1:1: [warning] missing document start \"---\" (document-start)\n")}
		case strings.HasPrefix(j, "ansible-lint"):
			return Output{Stdout: []byte("[]")}
		}
		return Output{}
	}
	return fx
}

func TestRunAllNormalisesPersistsAndReports(t *testing.T) {
	e := newEnv(t, realisticExec())
	if err := e.svc.Run(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	ds, err := e.svc.Diagnostics("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var validate, fmtD, yl int
	var fix *QuickFix
	for _, d := range ds {
		switch d.Tool {
		case "validate":
			validate++
			if d.Fix != nil {
				fix = d.Fix
			}
			if d.Unit != "tf:terraform/envs/dev" || d.File != "terraform/envs/dev/main.tf" {
				t.Errorf("validate diag mislocated: %+v", d)
			}
		case "fmt":
			fmtD++
			if d.Fix == nil || d.Fix.Kind != "fmt" {
				t.Errorf("fmt diag lacks fix: %+v", d)
			}
		case "yamllint":
			yl++
		}
	}
	if validate != 2 || fmtD != 1 || yl != 1 {
		t.Fatalf("validate=%d fmt=%d yamllint=%d: %+v", validate, fmtD, yl, ds)
	}
	if fix == nil || fix.Edits[0].New != "triggers_replace" {
		t.Fatalf("did-you-mean fix missing: %+v", fix)
	}

	// statuses
	byUnit := map[string]UnitStatus{}
	for _, u := range e.svc.Status() {
		byUnit[u.ID] = u
	}
	dev := byUnit["tf:terraform/envs/dev"]
	got := map[string]string{}
	for _, r := range dev.Results {
		got[r.Tool] = r.Status
	}
	if got["validate"] != "issues" || got["fmt"] != "issues" || got["tflint"] != "ok" {
		t.Fatalf("dev statuses %v", got)
	}
	if byUnit["tf:terraform/envs/prod"].Results[1].Status != "ok" {
		t.Fatalf("prod validate: %+v", byUnit["tf:terraform/envs/prod"].Results)
	}

	// commands: terraform runs in a private data dir and never touches real state/backends
	for _, c := range e.fx.calls {
		j := strings.Join(c.Argv, " ")
		if strings.HasPrefix(j, "terraform") {
			var dataDir string
			for _, kv := range c.Env {
				if v, ok := strings.CutPrefix(kv, "TF_DATA_DIR="); ok {
					dataDir = v
				}
			}
			if !strings.Contains(dataDir, filepath.Join(".groundwork", "tfdata")) {
				t.Errorf("terraform call without private TF_DATA_DIR: %v env=%v", c.Argv, c.Env)
			}
			if strings.Contains(j, " apply") || strings.Contains(j, " plan") {
				t.Errorf("checks must never plan/apply: %s", j)
			}
		}
	}
	if e.fx.count("init -backend=false") != 0 {
		t.Error("init should not run when validate works without it")
	}

	// events
	var started, updated int
	for len(e.sub) > 0 {
		switch (<-e.sub).Type {
		case "checks.started":
			started++
		case "checks.updated":
			updated++
		}
	}
	if started != len(e.svc.Units()) || updated < started {
		t.Fatalf("events started=%d updated=%d", started, updated)
	}
}

func TestMissingToolIsSkippedWithHintAndClearsStale(t *testing.T) {
	fx := realisticExec()
	fx.missing = map[string]bool{"tflint": true, "yamllint": true}
	e := newEnv(t, fx)
	e.st.ReplaceDiagnostics("tf:terraform/envs/dev", "tflint", "old", []store.DiagRow{{Severity: "warning", Message: "stale", File: "x"}})
	e.svc.Run(context.Background(), "tf:terraform/envs/dev", false)
	var tl ToolStatus
	for _, u := range e.svc.Status() {
		if u.ID == "tf:terraform/envs/dev" {
			for _, r := range u.Results {
				if r.Tool == "tflint" {
					tl = r
				}
			}
		}
	}
	if tl.Status != "skipped" || tl.Hint != "brew install tflint" || !strings.Contains(tl.Message, "not found") {
		t.Fatalf("%+v", tl)
	}
	ds, _ := e.svc.Diagnostics("tf:terraform/envs/dev", "", "")
	for _, d := range ds {
		if d.Tool == "tflint" {
			t.Fatal("stale diagnostics from an uninstalled tool should be cleared")
		}
	}
}

func TestInitRetryWhenValidateNeedsIt(t *testing.T) {
	validates := 0
	initialised := false // like a real data dir: init fixes "Module not installed"
	fx := &fakeExec{}
	fx.handler = func(c Cmd) Output {
		j := strings.Join(c.Argv, " ")
		if strings.Contains(j, " init ") {
			initialised = true
		}
		if strings.Contains(j, " validate ") {
			validates++
			if !initialised {
				return Output{ExitCode: 1, Stdout: []byte(`{"valid":false,"error_count":1,"diagnostics":[{"severity":"error","summary":"Module not installed","detail":"x","range":{"filename":"main.tf","start":{"line":7,"column":1},"end":{"line":7,"column":20}}}]}`)}
			}
			return Output{Stdout: []byte(`{"valid":true,"diagnostics":[]}`)}
		}
		return Output{}
	}
	e := newEnv(t, fx)
	e.svc.Run(context.Background(), "tf:terraform/envs/prod", false)
	if validates != 2 || fx.count("init -backend=false -input=false") != 1 {
		t.Fatalf("validates=%d inits=%d", validates, fx.count("init"))
	}
	ds, _ := e.svc.Diagnostics("tf:terraform/envs/prod", "", "")
	for _, d := range ds {
		if d.Tool == "validate" {
			t.Fatalf("an init-needed error must not be shown as a code error: %+v", d)
		}
	}
	// second run (force): the data dir is already initialised, so no second init
	validates = 0
	e.svc.Run(context.Background(), "tf:terraform/envs/prod", true)
	if validates != 1 || fx.count("init -backend=false") != 1 {
		t.Fatalf("second run: validates=%d inits=%d", validates, fx.count("init"))
	}
}

func TestInitFailureIsFailedStatusNotDiagnostics(t *testing.T) {
	fx := &fakeExec{}
	fx.handler = func(c Cmd) Output {
		j := strings.Join(c.Argv, " ")
		switch {
		case strings.Contains(j, " validate "):
			return Output{ExitCode: 1, Stdout: []byte(`{"valid":false,"diagnostics":[{"severity":"error","summary":"Missing required provider","detail":"d"}]}`)}
		case strings.Contains(j, " init "):
			return Output{ExitCode: 1, Stderr: []byte("\nError: Failed to query available provider packages\n\ncould not connect to registry.terraform.io\n")}
		}
		return Output{}
	}
	e := newEnv(t, fx)
	// a previous good result must survive a failed run
	e.st.ReplaceDiagnostics("tf:terraform/envs/dev", "validate", "old", []store.DiagRow{{Severity: "error", Message: "kept", File: "a"}})
	e.svc.Run(context.Background(), "tf:terraform/envs/dev", false)
	for _, u := range e.svc.Status() {
		if u.ID != "tf:terraform/envs/dev" {
			continue
		}
		for _, r := range u.Results {
			if r.Tool == "validate" {
				if r.Status != "failed" || !strings.Contains(r.Message, "Failed to query available provider packages") || r.Hint == "" {
					t.Fatalf("%+v", r)
				}
			}
		}
	}
	ds, _ := e.svc.Diagnostics("tf:terraform/envs/dev", "", "")
	kept := false
	for _, d := range ds {
		if d.Message == "kept" {
			kept = true
		}
	}
	if !kept {
		t.Fatal("a failed run must not wipe the last good diagnostics")
	}
}

func TestCacheSkipsUnchangedAndRerunsOnEdit(t *testing.T) {
	e := newEnv(t, realisticExec())
	ctx := context.Background()
	e.svc.Run(ctx, "tf:terraform/envs/prod", false)
	first := len(e.fx.calls)
	e.svc.Run(ctx, "tf:terraform/envs/prod", false)
	if len(e.fx.calls) != first {
		t.Fatalf("unchanged unit re-ran tools: %d → %d calls", first, len(e.fx.calls))
	}
	e.svc.Run(ctx, "tf:terraform/envs/prod", true)
	if len(e.fx.calls) == first {
		t.Fatal("force must bypass the cache")
	}
	n := len(e.fx.calls)
	os.WriteFile(filepath.Join(e.root, "terraform/envs/prod/main.tf"), []byte("# changed\n"), 0o644)
	e.svc.Run(ctx, "tf:terraform/envs/prod", false)
	if len(e.fx.calls) == n {
		t.Fatal("edit must invalidate the cache")
	}
	// editing the shared module invalidates every root that uses it
	e.svc.Run(ctx, "tf:terraform/envs/dev", false)
	n = len(e.fx.calls)
	e.svc.Run(ctx, "tf:terraform/envs/dev", false)
	if len(e.fx.calls) != n {
		t.Fatal("expected cache hit")
	}
	os.WriteFile(filepath.Join(e.root, "terraform/modules/network/outputs.tf"), []byte("# m\n"), 0o644)
	e.svc.Run(ctx, "tf:terraform/envs/dev", false)
	if len(e.fx.calls) == n {
		t.Fatal("module edit must invalidate dependent root")
	}
}

func TestOnChangeDebouncesBurstsAndMapsToUnits(t *testing.T) {
	e := newEnv(t, realisticExec())
	// wide next to the ~25ms burst: on a loaded CI runner a stall between two
	// calls would rightly split a narrow window into two runs
	e.svc.debounce = 300 * time.Millisecond
	for i := 0; i < 5; i++ {
		e.svc.OnChange([]string{"terraform/modules/network/main.tf"})
		time.Sleep(5 * time.Millisecond)
	}
	e.svc.OnChange([]string{".git/index", ".groundwork/state.db", "README.md", "terraform/envs/dev/.terraform/x.tf"})
	for i := 0; i < 150 && (e.fx.count("terraform/envs/dev validate") == 0 || e.fx.count("terraform/envs/prod validate") == 0); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(e.svc.debounce + 100*time.Millisecond) // a second run would have started by now
	// module edit → module unit + both roots, once each
	if got := e.fx.count("terraform/envs/dev validate"); got != 1 {
		t.Fatalf("dev validated %d times after a burst, want 1", got)
	}
	if got := e.fx.count("terraform/envs/prod validate"); got != 1 {
		t.Fatalf("prod validated %d times, want 1", got)
	}
	if got := e.fx.count("--syntax-check"); got != 0 {
		t.Fatalf("ansible must not run for a terraform change (%d)", got)
	}
}

func TestCloseStopsBackgroundRuns(t *testing.T) {
	fx := realisticExec()
	inner := fx.handler
	fx.handler = func(c Cmd) Output { time.Sleep(50 * time.Millisecond); return inner(c) }
	e := newEnv(t, fx)
	e.svc.OnChange([]string{"terraform/envs/prod/main.tf"}) // debounced: still pending
	e.svc.RunNow([]string{"terraform/envs/dev/main.tf"})    // in flight
	time.Sleep(10 * time.Millisecond)
	e.svc.Close() // returns once the in-flight run is done
	settled := e.fx.count(" ")
	e.svc.RunNow([]string{"terraform/envs/dev/main.tf"})
	time.Sleep(150 * time.Millisecond)
	if got := e.fx.count(" "); got != settled {
		t.Fatalf("%d commands ran after Close", got-settled)
	}
	if got := e.fx.count("terraform/envs/prod validate"); got != 0 {
		t.Fatalf("the pending debounced run should have been dropped (%d)", got)
	}
}

func TestOnChangeRespectsOnSaveFalse(t *testing.T) {
	e := newEnv(t, realisticExec())
	e.svc.Cfg().Checks.OnSave = false
	e.svc.OnChange([]string{"terraform/envs/dev/main.tf"})
	time.Sleep(150 * time.Millisecond)
	if len(e.fx.calls) != 0 {
		t.Fatal("on_save=false must not trigger runs")
	}
}

func TestSyntaxCheckPerPlaybook(t *testing.T) {
	fx := &fakeExec{}
	fx.handler = func(c Cmd) Output {
		if strings.Contains(strings.Join(c.Argv, " "), "--syntax-check") {
			return Output{ExitCode: 4, Stderr: []byte(gold("syntax_yaml_error.txt"))}
		}
		return Output{}
	}
	e := newEnv(t, fx)
	e.svc.realRoot = "/repo"
	e.svc.Run(context.Background(), "ans:ansible", false)
	ds, _ := e.svc.Diagnostics("ans:ansible", "", "")
	var syn *Diagnostic
	for i := range ds {
		if ds[i].Tool == "syntax-check" {
			syn = &ds[i]
		}
	}
	if syn == nil || syn.File != "ansible/broken.yml" || syn.Line != 6 {
		t.Fatalf("%+v", ds)
	}
	for _, c := range fx.calls {
		if strings.Contains(strings.Join(c.Argv, " "), "--syntax-check") {
			if c.Dir != filepath.Join(e.root, "ansible") || c.Argv[len(c.Argv)-1] != "playbooks/site.yml" {
				t.Fatalf("syntax-check should run in the project dir on a project-relative playbook: %+v", c)
			}
		}
	}
}

func TestFormatFile(t *testing.T) {
	fx := &fakeExec{}
	e := newEnv(t, fx)
	if err := e.svc.FormatFile(context.Background(), "terraform/envs/dev/main.tf"); err != nil {
		t.Fatal(err)
	}
	last := fx.calls[len(fx.calls)-1].Argv
	if last[1] != "fmt" || last[2] != filepath.Join(e.root, "terraform/envs/dev/main.tf") {
		t.Fatalf("%v", last)
	}
	if err := e.svc.FormatFile(context.Background(), "ansible/playbooks/site.yml"); err == nil {
		t.Fatal("only terraform files can be formatted")
	}
	fx.missing = map[string]bool{"terraform": true}
	if err := e.svc.FormatFile(context.Background(), "a.tf"); err == nil || !strings.Contains(err.Error(), "brew install terraform") {
		t.Fatalf("%v", err)
	}
}

func TestShadowTreeMirrorsRepoWithoutTouchingIt(t *testing.T) {
	e := newEnv(t, &fakeExec{})
	u, _ := e.svc.unit("tf:terraform/envs/dev")
	os.WriteFile(filepath.Join(e.root, "terraform/envs/dev/.terraform.lock.hcl"), []byte("# user lock\n"), 0o644)

	dir, err := e.svc.shadowDir(u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dir, filepath.Join(".groundwork", "shadow")) {
		t.Fatalf("shadow outside .groundwork: %s", dir)
	}
	// own files resolve, a sibling module is reachable via the relative path modules use
	if _, err := os.Stat(filepath.Join(dir, "main.tf")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "../../modules/network/main.tf")); err != nil {
		t.Fatalf("relative module path must resolve in the shadow: %v", err)
	}
	// unrelated top-level content is visible too (file("../../x") style references)
	if _, err := os.Stat(filepath.Join(dir, "../../../README.md")); err != nil {
		t.Fatalf("repo content should be visible: %v", err)
	}
	// ...but groundwork's own state is not mirrored into itself
	if _, err := os.Lstat(filepath.Join(dir, "../../../.groundwork")); err == nil {
		t.Fatal(".groundwork must not be mirrored")
	}
	// the unit dir is real, holds a copy of the user's lock, and writing there never reaches the repo
	if st, _ := os.Lstat(dir); st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("unit dir must be a real directory")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, lockFile)); string(b) != "# user lock\n" {
		t.Fatalf("lock not copied: %q", b)
	}
	os.WriteFile(filepath.Join(dir, lockFile), []byte("# terraform rewrote this\n"), 0o644)
	if b, _ := os.ReadFile(filepath.Join(e.root, "terraform/envs/dev", lockFile)); string(b) != "# user lock\n" {
		t.Fatal("writing the shadow lock changed the repo's lock")
	}

	// without a user lock, one created by init persists across runs but is never copied out
	os.Remove(filepath.Join(e.root, "terraform/envs/dev", lockFile))
	os.WriteFile(filepath.Join(dir, lockFile), []byte("# made by init\n"), 0o644)
	dir2, _ := e.svc.shadowDir(u)
	if b, _ := os.ReadFile(filepath.Join(dir2, lockFile)); string(b) != "# made by init\n" {
		t.Fatalf("init's lock should persist in the shadow: %q", b)
	}
	if _, err := os.Stat(filepath.Join(e.root, "terraform/envs/dev", lockFile)); err == nil {
		t.Fatal("lock leaked into the repo")
	}
}

func TestValidateRunsInShadowAndDiagnosticsMapBackToRepoPaths(t *testing.T) {
	fx := &fakeExec{}
	fx.handler = func(c Cmd) Output {
		if strings.Contains(strings.Join(c.Argv, " "), " validate ") {
			// a module file, reported relative to the shadow chdir like real terraform does
			return Output{ExitCode: 1, Stdout: []byte(`{"valid":false,"diagnostics":[{"severity":"error","summary":"boom","detail":"d","range":{"filename":"../../modules/network/main.tf","start":{"line":2,"column":3},"end":{"line":2,"column":9}}}]}`)}
		}
		return Output{}
	}
	e := newEnv(t, fx)
	e.svc.Run(context.Background(), "tf:terraform/envs/dev", false)
	for _, c := range fx.calls {
		j := strings.Join(c.Argv, " ")
		if strings.Contains(j, " validate ") || strings.Contains(j, " init ") {
			if !strings.Contains(j, filepath.Join(".groundwork", "shadow")) {
				t.Errorf("terraform ran against the real tree: %s", j)
			}
		}
		if strings.Contains(j, " fmt ") && strings.Contains(j, "shadow") {
			t.Errorf("fmt should check the real files: %s", j)
		}
	}
	ds, _ := e.svc.Diagnostics("tf:terraform/envs/dev", "", "")
	for _, d := range ds {
		if d.Tool == "validate" && d.File != "terraform/modules/network/main.tf" {
			t.Fatalf("module diagnostic not mapped to a repo path: %+v", d)
		}
	}
}

func TestTflintRunsInTheUnitDirWithoutChdir(t *testing.T) {
	fx := &fakeExec{}
	e := newEnv(t, fx)
	e.svc.Cfg().Checks.Enabled = append(e.svc.Cfg().Checks.Enabled, "tflint")
	e.svc.dirty = true
	e.svc.Run(context.Background(), "tf:terraform/envs/dev", true)
	found := false
	for _, c := range fx.calls {
		if c.Argv[0] == "tflint" {
			found = true
			if c.Dir != filepath.Join(e.root, "terraform/envs/dev") {
				t.Errorf("tflint cwd %q, want the unit dir", c.Dir)
			}
			for _, a := range c.Argv {
				if strings.HasPrefix(a, "--chdir") {
					t.Errorf("--chdir makes tflint print symlink-dependent paths: %v", c.Argv)
				}
			}
		}
	}
	if !found {
		t.Fatal("tflint not run")
	}
}

func TestDiagnosticsNeverEscapeTheRepo(t *testing.T) {
	e := newEnv(t, &fakeExec{})
	u, _ := e.svc.unit("tf:terraform/envs/dev")
	cases := map[string]string{
		"../../../../../../private/var/x/terraform/envs/dev/main.tf": u.Path, // junk relative path → unit
		"/somewhere/else/main.tf":                                    u.Path,
		"terraform/envs/dev/main.tf":                                 "terraform/envs/dev/main.tf", // fine as is
		filepath.Join(e.root, "terraform/envs/dev/a.tf"):             "terraform/envs/dev/a.tf",    // absolute inside repo
	}
	for in, want := range cases {
		d := Diagnostic{File: in, Line: 4, Col: 2}
		e.svc.normalise(u, &d)
		if d.File != want {
			t.Errorf("%q → %q, want %q", in, d.File, want)
		}
		if want == u.Path && (d.Line != 0 || !strings.Contains(d.Detail, in)) {
			t.Errorf("unlocatable finding should drop its position and keep the original in detail: %+v", d)
		}
	}
}

func TestInitsNeverOverlap(t *testing.T) {
	var mu sync.Mutex
	active, maxActive := 0, 0
	fx := &fakeExec{}
	fx.handler = func(c Cmd) Output {
		j := strings.Join(c.Argv, " ")
		switch {
		case strings.Contains(j, " init "):
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			time.Sleep(40 * time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
		case strings.Contains(j, " validate "):
			return Output{Stdout: []byte(`{"valid":false,"diagnostics":[{"severity":"error","summary":"Missing required provider","detail":"d"}]}`)}
		}
		return Output{}
	}
	e := newEnv(t, fx)
	e.svc.Run(context.Background(), "", true) // every root validates in parallel
	if maxActive != 1 {
		t.Fatalf("%d inits ran at once; terraform's shared plugin cache needs them serialised", maxActive)
	}
	if fx.count(" init ") < 2 {
		t.Fatal("expected several roots to init")
	}
}

func TestUserPluginCacheIsRespected(t *testing.T) {
	t.Setenv("TF_PLUGIN_CACHE_DIR", "/users/own/cache")
	e := newEnv(t, &fakeExec{})
	u, _ := e.svc.unit("tf:terraform/envs/dev")
	for _, kv := range e.svc.tfEnv(u) {
		if strings.HasPrefix(kv, "TF_PLUGIN_CACHE_DIR=") {
			t.Fatalf("must not override the user's cache: %s", kv)
		}
	}
}

func TestOverlappingRunsOfOneUnitNeverBreakTheShadowTree(t *testing.T) {
	var mu sync.Mutex
	inPlace := 0
	fx := &fakeExec{}
	e := newEnv(t, fx)
	fx.handler = func(c Cmd) Output {
		j := strings.Join(c.Argv, " ")
		if strings.Contains(j, " validate ") {
			if !strings.Contains(j, filepath.Join(".groundwork", "shadow")) {
				mu.Lock()
				inPlace++ // terraform pointed at the real tree: init could drop a lock file in the repo
				mu.Unlock()
			}
			time.Sleep(5 * time.Millisecond)
			return Output{Stdout: []byte(`{"valid":true,"diagnostics":[]}`)}
		}
		if strings.HasPrefix(j, "tflint") {
			return Output{Stdout: []byte(`{"issues":[],"errors":[]}`)}
		}
		return Output{}
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.svc.Run(context.Background(), "tf:terraform/envs/dev", true) // a newer run cancels an older one mid-flight
		}()
	}
	wg.Wait()
	if inPlace != 0 {
		t.Fatalf("%d validate calls ran against the real tree after a shadow-tree race", inPlace)
	}
	for _, u := range e.svc.Status() {
		for _, r := range u.Results {
			if r.Status == "failed" {
				t.Fatalf("%s %s failed: %s", u.ID, r.Tool, r.Message)
			}
		}
	}
}
