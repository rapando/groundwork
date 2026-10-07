package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

func newAPI(t *testing.T, root string) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	New(root, "test", events.NewBus(), st).Routes(r)
	return r
}

func call(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func into(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return m
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
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

func fixture(t *testing.T, name string) string {
	dir := t.TempDir()
	copyDir(t, "../../testdata/repos/"+name, dir)
	return dir
}

func goodForm() map[string]any {
	return map[string]any{
		"preset": "aws-envs", "project": "acme", "backend": "s3", "bucket": "acme-tfstate",
		"region": "eu-west-1", "envs": []string{"dev", "prod"}, "layout": "dirs",
		"inventory": "terraform", "roles": []string{"common"},
		"checks": []string{"fmt", "validate", "tflint"}, "ci": true,
	}
}

func TestWorkspaceSetupModeRouting(t *testing.T) {
	empty := into(t, call(t, newAPI(t, t.TempDir()), "GET", "/workspace", nil))
	if empty["setup"] != "scaffold" || empty["configured"] != false {
		t.Fatalf("empty: %v", empty)
	}
	h := newAPI(t, fixture(t, "iac-only"))
	if m := into(t, call(t, h, "GET", "/workspace", nil)); m["setup"] != "detect" {
		t.Fatalf("iac repo: %v", m)
	}
	if m := into(t, call(t, newAPI(t, fixture(t, "app-only")), "GET", "/workspace", nil)); m["setup"] != "scaffold" {
		t.Fatalf("app without IaC should offer scaffold: %v", m)
	}
}

func TestDetectApplyThenConfigured(t *testing.T) {
	root := fixture(t, "embedded")
	h := newAPI(t, root)

	d := into(t, call(t, h, "GET", "/setup/detect", nil))
	if !strings.Contains(d["config_yaml"].(string), "deploy/terraform") || d["exists"] != false {
		t.Fatalf("%v", d)
	}
	// no body → uses the detected config; ticks one lint config
	rec := call(t, h, "POST", "/setup/apply", map[string]any{
		"lint_configs": []map[string]string{{"kind": "yamllint", "dir": "ops/ansible"}, {"kind": "tflint", "dir": "deploy/terraform"}},
		"gitignore":    true,
	})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, f := range []string{"groundwork.yaml", "ops/ansible/.yamllint", "deploy/terraform/.tflint.hcl", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	// embedded mode must not add anything else
	if _, err := os.Stat(filepath.Join(root, "terraform")); err == nil {
		t.Error("unexpected scaffold in embedded repo")
	}
	w := into(t, call(t, h, "GET", "/workspace", nil))
	if w["configured"] != true || w["setup"] != "none" || w["mode"] != "embedded" {
		t.Fatalf("%v", w)
	}
	if call(t, h, "POST", "/setup/apply", map[string]any{}).Code != 409 {
		t.Fatal("second apply should conflict")
	}
}

func TestApplyInvalidYAMLReportsPositions(t *testing.T) {
	h := newAPI(t, fixture(t, "iac-only"))
	y := "version: 1\nmode: standalone\nterraform:\n  roots:\n    - path: ../x\n      env: dev\n"
	rec := call(t, h, "POST", "/setup/apply", map[string]any{"yaml": y})
	if rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e := into(t, rec)["error"].(map[string]any)
	probs := e["details"].([]any)
	if len(probs) == 0 || probs[0].(map[string]any)["line"].(float64) != 5 {
		t.Fatalf("%v", e)
	}
}

func TestScaffoldPreviewAndWrite(t *testing.T) {
	root := t.TempDir()
	h := newAPI(t, root)

	bad := goodForm()
	bad["region"] = "nowhere"
	rec := call(t, h, "POST", "/setup/scaffold/preview", map[string]any{"form": bad})
	if rec.Code != 422 {
		t.Fatalf("%d", rec.Code)
	}

	rec = call(t, h, "POST", "/setup/scaffold/preview", map[string]any{"form": goodForm()})
	m := into(t, rec)
	if rec.Code != 200 || m["count"].(float64) < 20 || m["existing"].(float64) != 0 {
		t.Fatalf("%d %v", rec.Code, m["count"])
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("preview must not write")
	}

	// zip: streams, writes nothing
	rec = call(t, h, "POST", "/setup/scaffold?format=zip", map[string]any{"form": goodForm()})
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || len(zr.File) < 20 {
		t.Fatalf("zip: %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("zip must not write")
	}

	rec = call(t, h, "POST", "/setup/scaffold", map[string]any{"form": goodForm()})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if w := into(t, call(t, h, "GET", "/workspace", nil)); w["configured"] != true {
		t.Fatalf("scaffold should leave a valid config: %v", w)
	}
}

func TestScaffoldGitCommitOnlyScaffoldFiles(t *testing.T) {
	// git commit spawns detached auto-maintenance that can still be writing into .git
	// when the temp dir is removed; keep the test hermetic.
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	t.Setenv("GIT_CONFIG_VALUE_0", "0")
	t.Setenv("GIT_CONFIG_KEY_1", "maintenance.auto")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	root := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(root, "wip.txt"), []byte("x"), 0o644)
	git("add", "wip.txt") // user's staged work must not be swept in

	h := newAPI(t, root)
	rec := call(t, h, "POST", "/setup/scaffold", map[string]any{"form": goodForm(), "git_commit": true})
	m := into(t, rec)
	if rec.Code != 200 || m["git_error"] != nil {
		t.Fatalf("%d %v", rec.Code, m)
	}
	if files := git("show", "--name-only", "--format=", "HEAD"); strings.Contains(files, "wip.txt") || !strings.Contains(files, "groundwork.yaml") {
		t.Fatalf("commit contents:\n%s", files)
	}
	if st := git("status", "--porcelain"); !strings.Contains(st, "wip.txt") {
		t.Fatalf("wip.txt should still be staged: %q", st)
	}

	// not a git repo → refused before writing
	plain := t.TempDir()
	rec = call(t, newAPI(t, plain), "POST", "/setup/scaffold", map[string]any{"form": goodForm(), "git_commit": true})
	if rec.Code != 400 {
		t.Fatalf("%d", rec.Code)
	}
	if entries, _ := os.ReadDir(plain); len(entries) != 0 {
		t.Fatal("must not write when commit impossible")
	}
}

func TestRejectsUnknownFields(t *testing.T) {
	rec := call(t, newAPI(t, t.TempDir()), "POST", "/setup/scaffold/preview", map[string]any{"form": goodForm(), "nope": 1})
	if rec.Code != 400 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestSetupConfigSelection(t *testing.T) {
	h := newAPI(t, fixture(t, "iac-only"))
	rec := call(t, h, "POST", "/setup/config", map[string]any{"exclude": []string{"ansible"}, "prod_approval": true})
	y := into(t, rec)["config_yaml"].(string)
	if strings.Contains(y, "ansible:") || !strings.Contains(y, "approval: required") {
		t.Fatalf("%s", y)
	}
	if strings.Count(y, "approval:") != 1 {
		t.Fatalf("only prod should carry approval:\n%s", y)
	}
	rec = call(t, h, "POST", "/setup/config", map[string]any{"exclude": []string{"terraform/envs/dev"}, "prod_approval": false})
	y = into(t, rec)["config_yaml"].(string)
	if strings.Contains(y, "terraform/envs/dev") || !strings.Contains(y, "approval: none") {
		t.Fatalf("%s", y)
	}
	// what the preview returns must be writable as-is
	if rec := call(t, h, "POST", "/setup/apply", map[string]any{"yaml": y}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestApplyLintDirMustStayInRepo(t *testing.T) {
	h := newAPI(t, fixture(t, "iac-only"))
	rec := call(t, h, "POST", "/setup/apply", map[string]any{
		"lint_configs": []map[string]string{{"kind": "yamllint", "dir": "../.."}},
	})
	if rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
