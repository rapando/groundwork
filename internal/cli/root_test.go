package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/doctor"
)

// exec runs the command tree the way the binary does, with a temp data
// directory so no real service is touched.
func execCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("GROUNDWORK_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // no login unit of the developer's own machine
	root := newRoot()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestVersionCommand(t *testing.T) {
	Version, Commit = "1.2.3", "abc1234"
	t.Cleanup(func() { Version, Commit = "dev", "none" })
	out, _, err := execCLI(t, "version")
	if err != nil || !strings.Contains(out, "groundwork 1.2.3") || !strings.Contains(out, "abc1234") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestInitDetectWritesConfigOnce(t *testing.T) {
	dir := repo(t, false)
	out, _, err := execCLI(t, "init", "--detect", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "groundwork.yaml"))
	if err != nil || !strings.Contains(string(b), "terraform/envs/dev") {
		t.Fatalf("config not written: %v %s", err, b)
	}
	// a second init refuses to overwrite
	if _, _, err := execCLI(t, "init", "--detect", "--yes"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init: %v", err)
	}
	// --dry-run on a fresh copy writes nothing
	dir2 := repo(t, false)
	if _, _, err := execCLI(t, "init", "--detect", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "groundwork.yaml")); err == nil {
		t.Fatal("dry run wrote the config")
	}
}

type fakeDoctorExec struct{ have map[string]bool }

func (f fakeDoctorExec) LookPath(n string) (string, error) {
	if f.have[n] {
		return "/usr/bin/" + n, nil
	}
	return "", os.ErrNotExist
}
func (f fakeDoctorExec) Run(_ context.Context, _ string, argv ...string) (string, string, int, error) {
	switch argv[0] {
	case "terraform":
		return "Terraform v1.3.0\n", "", 0, nil // below the floor: a warning
	case "ansible":
		return "ansible [core 2.21.0]", "", 0, nil
	case "git":
		return "git version 2.44.0", "", 0, nil
	}
	return "", "", 1, nil
}

var _ doctor.Exec = fakeDoctorExec{}

func TestDoctorJSONReportsFloorsAndMissingTools(t *testing.T) {
	repo(t, true)
	t.Setenv("GROUNDWORK_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFAKEFAKEFAKEFAKE") // the fixture uses the aws provider; aws CLI absent → not checked
	cmd := newDoctor(fakeDoctorExec{have: map[string]bool{"terraform": true, "ansible": true, "git": true}})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	err := cmd.ExecuteContext(context.Background())
	var rep struct {
		Checks []struct{ ID, Status, Detail string }
	}
	if jerr := json.Unmarshal(out.Bytes(), &rep); jerr != nil {
		t.Fatalf("not JSON: %v\n%s", jerr, out.String())
	}
	byID := map[string]struct{ ID, Status, Detail string }{}
	for _, c := range rep.Checks {
		byID[c.ID] = c
	}
	if c := byID["tool:terraform"]; c.Status != "warn" || !strings.Contains(c.Detail, "1.3.0") {
		t.Fatalf("floor warning: %+v", c)
	}
	// yamllint is an enabled check but not installed: a warning, and the command still exits 0
	if c := byID["tool:yamllint"]; c.Status != "warn" {
		t.Fatalf("missing optional tool: %+v", c)
	}
	if err != nil {
		var failed []string
		for _, c := range rep.Checks {
			if c.Status == "fail" {
				failed = append(failed, c.ID+": "+c.Detail)
			}
		}
		t.Fatalf("warnings alone should not fail doctor: %v; failing checks: %v", err, failed)
	}
}

func TestServiceCommandsWithoutARunningService(t *testing.T) {
	out, _, err := execCLI(t, "service", "status")
	if err != nil || !strings.Contains(out, "status:        stopped") || !strings.Contains(out, "login service: not installed") {
		t.Fatalf("%q %v", out, err)
	}
	if _, _, err := execCLI(t, "service", "stop"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("stop: %v", err)
	}
	if _, _, err := execCLI(t, "service", "logs"); err == nil {
		t.Fatal("logs without a log file should fail")
	}
	// with a log file, the last lines are printed
	home := t.TempDir()
	t.Setenv("GROUNDWORK_HOME", home)
	os.WriteFile(filepath.Join(home, "groundwork.log"), []byte("one\ntwo\nthree\n"), 0o600)
	root := newRoot()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"service", "logs", "-n", "2"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "two\nthree\n" {
		t.Fatalf("logs: %q", buf.String())
	}
}

func TestUnknownCommandAndHelp(t *testing.T) {
	if _, _, err := execCLI(t, "frobnicate"); err == nil {
		t.Fatal("unknown command accepted")
	}
	out, _, err := execCLI(t, "--help")
	if err != nil || !strings.Contains(out, "service") || !strings.Contains(out, "doctor") || !strings.Contains(out, "--log-level") {
		t.Fatalf("%v\n%s", err, out)
	}
}
