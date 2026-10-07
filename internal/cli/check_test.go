package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/workspace"
)

type fakeExec struct {
	missing map[string]bool
	handler func(argv string) checks.Output
}

func (f fakeExec) LookPath(n string) (string, error) {
	if f.missing[n] {
		return "", errors.New("missing")
	}
	return "/bin/" + n, nil
}
func (f fakeExec) Run(_ context.Context, c checks.Cmd) (checks.Output, error) {
	argv := strings.Join(c.Argv, " ")
	if f.handler != nil {
		if o := f.handler(argv); o.Stdout != nil || o.Stderr != nil || o.ExitCode != 0 {
			return o, nil
		}
	}
	if strings.Contains(argv, " validate ") { // real terraform always prints JSON here
		return checks.Output{Stdout: []byte(`{"valid":true,"diagnostics":[]}`)}, nil
	}
	return checks.Output{}, nil
}

const typoDiag = `{"valid":false,"diagnostics":[{"severity":"error","summary":"Unsupported argument","detail":"An argument named \"x\" is not expected here.","range":{"filename":"main.tf","start":{"line":3,"column":5},"end":{"line":3,"column":6}}}]}`

var fixtureDir, _ = filepath.Abs("../../testdata/repos/iac-only") // absolute: tests Chdir

// repo copies the iac-only fixture into a temp dir, writes a groundwork.yaml, and cd's there.
func repo(t *testing.T, configure bool) string {
	t.Helper()
	dir := t.TempDir()
	filepath.WalkDir(fixtureDir, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(fixtureDir, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	if configure {
		rep, _ := workspace.Detect(dir, nil)
		cfg := config.FromReport(rep)
		cfg.Checks.Enabled = []string{"fmt", "validate", "yamllint", "syntax-check"}
		y, _ := cfg.Marshal()
		os.WriteFile(filepath.Join(dir, "groundwork.yaml"), y, 0o644)
	}
	t.Chdir(dir)
	return dir
}

func run(t *testing.T, ex checks.Exec, f checkFlags) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runCheck(context.Background(), &out, ex, &f)
	return out.String(), err
}

func code(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	if err == nil {
		return 0
	}
	return -1
}

func TestCheckRequiresConfig(t *testing.T) {
	repo(t, false)
	_, err := run(t, fakeExec{}, checkFlags{failOn: "error"})
	if code(err) != 2 || !strings.Contains(err.Error(), "groundwork init") {
		t.Fatalf("%v", err)
	}
}

func TestCheckCleanExitsZero(t *testing.T) {
	repo(t, true)
	out, err := run(t, fakeExec{}, checkFlags{failOn: "error"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "0 errors, 0 warnings") || !strings.Contains(out, "terraform/envs/dev") {
		t.Fatalf("%s", out)
	}
}

func TestCheckFindingsExitOneAndPrintLocation(t *testing.T) {
	repo(t, true)
	ex := fakeExec{handler: func(a string) checks.Output {
		if strings.Contains(a, "envs/dev validate") {
			return checks.Output{ExitCode: 1, Stdout: []byte(typoDiag)}
		}
		return checks.Output{}
	}}
	out, err := run(t, ex, checkFlags{failOn: "error"})
	if code(err) != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if !strings.Contains(out, "terraform/envs/dev/main.tf:3:5: error [validate] Unsupported argument") ||
		!strings.Contains(out, "1 errors") {
		t.Fatalf("%s", out)
	}
}

func TestCheckJSON(t *testing.T) {
	repo(t, true)
	ex := fakeExec{handler: func(a string) checks.Output {
		if strings.Contains(a, "envs/dev validate") {
			return checks.Output{ExitCode: 1, Stdout: []byte(typoDiag)}
		}
		return checks.Output{}
	}}
	out, err := run(t, ex, checkFlags{json: true, failOn: "error"})
	if code(err) != 1 {
		t.Fatalf("%v", err)
	}
	var got struct {
		Diagnostics []struct {
			Tool, File string
			Line       int
		}
		Units  []struct{ ID string }
		Counts map[string]int
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if got.Counts["error"] != 1 || len(got.Diagnostics) != 1 || got.Diagnostics[0].Line != 3 || len(got.Units) < 3 {
		t.Fatalf("%+v", got)
	}
	// a clean run must emit [] not null
	repo(t, true)
	out, _ = run(t, fakeExec{}, checkFlags{json: true, failOn: "error"})
	if !strings.Contains(out, `"diagnostics": []`) {
		t.Fatalf("empty diagnostics should be an array: %s", out)
	}
}

func TestCheckFailOnWarning(t *testing.T) {
	repo(t, true)
	ex := fakeExec{handler: func(a string) checks.Output {
		if strings.Contains(a, " fmt ") {
			return checks.Output{ExitCode: 3, Stdout: []byte("main.tf\n")}
		}
		return checks.Output{}
	}}
	if _, err := run(t, ex, checkFlags{failOn: "error"}); err != nil {
		t.Fatalf("warnings must not fail by default: %v", err)
	}
	repo(t, true)
	if _, err := run(t, ex, checkFlags{failOn: "warning"}); code(err) != 1 {
		t.Fatalf("--fail-on warning: %v", err)
	}
	if _, err := run(t, ex, checkFlags{failOn: "bogus"}); code(err) != 2 {
		t.Fatalf("bad flag: %v", err)
	}
}

func TestCheckToolFailureIsDistinctFromFindings(t *testing.T) {
	repo(t, true)
	ex := fakeExec{handler: func(a string) checks.Output {
		switch {
		case strings.Contains(a, " validate "):
			return checks.Output{ExitCode: 1, Stdout: []byte(`{"valid":false,"diagnostics":[{"severity":"error","summary":"Module not installed","detail":"d"}]}`)}
		case strings.Contains(a, " init "):
			return checks.Output{ExitCode: 1, Stderr: []byte("Error: could not reach registry\n")}
		}
		return checks.Output{}
	}}
	out, err := run(t, ex, checkFlags{failOn: "error"})
	if code(err) != 3 {
		t.Fatalf("want 3 (incomplete), got %v\n%s", err, out)
	}
	if !strings.Contains(out, "could not run") || !strings.Contains(out, "could not reach registry") {
		t.Fatalf("%s", out)
	}
}

func TestCheckSkippedToolIsNotAFailure(t *testing.T) {
	repo(t, true)
	out, err := run(t, fakeExec{missing: map[string]bool{"yamllint": true}}, checkFlags{failOn: "error"})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "yamllint skipped") || !strings.Contains(out, "pipx install yamllint") {
		t.Fatalf("%s", out)
	}
}

func TestCheckUnknownUnit(t *testing.T) {
	repo(t, true)
	if _, err := run(t, fakeExec{}, checkFlags{unit: "tf:nope", failOn: "error"}); code(err) != 2 {
		t.Fatalf("%v", err)
	}
	out, err := run(t, fakeExec{}, checkFlags{unit: "tf:terraform/envs/prod", failOn: "error"})
	if err != nil || strings.Contains(out, "envs/dev") {
		t.Fatalf("--unit must restrict output: %v\n%s", err, out)
	}
}
