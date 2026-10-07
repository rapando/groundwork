package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rapando/groundwork/internal/doctor"
)

// needsInit are validate errors that mean "run init first", not "your code is wrong".
var needsInit = []string{
	"Module not installed", "Missing required provider", "Inconsistent dependency lock file",
	"Module source has changed", "Required plugins are not installed", "Backend initialization required",
}

func initNeeded(ds []Diagnostic) bool {
	for _, d := range ds {
		for _, n := range needsInit {
			if strings.Contains(d.Message, n) {
				return true
			}
		}
	}
	return false
}

func (s *Service) binFor(tool string) string {
	switch tool {
	case "fmt", "validate":
		return s.tfBinary()
	case "syntax-check":
		return "ansible-playbook"
	}
	return tool
}

func (s *Service) tfEnv(u Unit) []string {
	sum := sha256.Sum256([]byte(u.Path))
	data := filepath.Join(s.Root, ".groundwork", "tfdata", hex.EncodeToString(sum[:4]))
	env := []string{"TF_DATA_DIR=" + data, "TF_IN_AUTOMATION=1", "TF_INPUT=0", "NO_COLOR=1", "CHECKPOINT_DISABLE=1"}
	// Respect a cache the user already configured (shared across repos); else keep one here.
	if os.Getenv("TF_PLUGIN_CACHE_DIR") == "" {
		cache := filepath.Join(s.Root, ".groundwork", "plugin-cache")
		_ = os.MkdirAll(cache, 0o755)
		env = append(env, "TF_PLUGIN_CACHE_DIR="+cache)
	}
	return env
}

func (s *Service) ansEnv() []string {
	home := filepath.Join(s.Root, ".groundwork", "ansible-home")
	_ = os.MkdirAll(home, 0o755)
	return []string{"ANSIBLE_HOME=" + home, "ANSIBLE_NOCOLOR=1", "NO_COLOR=1"}
}

func fail(msg string) ToolStatus { return ToolStatus{Status: "failed", Message: msg} }

// execTool runs one tool and returns normalised diagnostics plus a status
// (Status empty means "derive ok/issues from the diagnostics").
func (s *Service) execTool(ctx context.Context, u Unit, tool string) ([]Diagnostic, ToolStatus) {
	if tool == "secrets" {
		return s.scanSecrets(ctx, u), ToolStatus{}
	}
	bin := s.binFor(tool)
	if _, err := s.Exec.LookPath(bin); err != nil {
		return nil, ToolStatus{Status: "skipped", Message: bin + " not found", Hint: doctor.Hint(bin)}
	}
	dir := s.abs(u.Path)
	switch tool {
	case "fmt":
		out, err := s.Exec.Run(ctx, Cmd{Dir: s.Root, Argv: []string{bin, "-chdir=" + dir, "fmt", "-check", "-list=true"}, Env: s.tfEnv(u), Timeout: 30 * time.Second})
		if err != nil {
			return nil, fail(err.Error())
		}
		if out.ExitCode == 2 { // could not parse some file
			return ParseFmtList(out.Stdout, u.Path), ToolStatus{Message: firstLine(out.Stderr)}
		}
		return ParseFmtList(out.Stdout, u.Path), ToolStatus{}

	case "validate":
		return s.validate(ctx, u, bin, dir)

	case "tflint":
		// cwd = the unit: with --chdir tflint reports paths relative to its original
		// cwd, mixing in symlink-resolved prefixes (/var vs /private/var on macOS).
		out, err := s.Exec.Run(ctx, Cmd{Dir: dir, Argv: []string{bin, "--format=json"}, Timeout: 60 * time.Second})
		if err != nil {
			return nil, fail(err.Error())
		}
		ds, perr := ParseTflint(out.Stdout, u.Path)
		if perr != nil {
			return nil, fail("tflint: " + firstLine(append(out.Stderr, out.Stdout...)))
		}
		return ds, ToolStatus{}

	case "checkov":
		out, err := s.Exec.Run(ctx, Cmd{Dir: s.Root, Argv: []string{bin, "-d", dir, "-o", "json", "--quiet", "--framework", "terraform"}, Timeout: 3 * time.Minute})
		if err != nil {
			return nil, fail(err.Error())
		}
		if len(strings.TrimSpace(string(out.Stdout))) == 0 {
			if out.ExitCode != 0 {
				return nil, fail("checkov: " + firstLine(out.Stderr))
			}
			return nil, ToolStatus{}
		}
		ds, perr := ParseCheckov(out.Stdout, u.Path)
		if perr != nil {
			return nil, fail(perr.Error())
		}
		return ds, ToolStatus{}

	case "yamllint":
		out, err := s.Exec.Run(ctx, Cmd{Dir: dir, Argv: []string{bin, "-f", "parsable", "."}, Timeout: 30 * time.Second})
		if err != nil {
			return nil, fail(err.Error())
		}
		if out.ExitCode > 1 {
			return nil, fail("yamllint: " + firstLine(append(out.Stderr, out.Stdout...)))
		}
		return ParseYamllint(out.Stdout, u.Path), ToolStatus{}

	case "ansible-lint":
		out, err := s.Exec.Run(ctx, Cmd{Dir: dir, Argv: []string{bin, "-f", "json", "--nocolor"}, Env: s.ansEnv(), Timeout: 3 * time.Minute})
		if err != nil {
			return nil, fail(err.Error())
		}
		ds, perr := ParseAnsibleLint(out.Stdout, u.Path)
		if perr != nil || (out.ExitCode > 2) {
			return nil, fail("ansible-lint: " + lastNonWarning(out.Stderr))
		}
		return ds, ToolStatus{}

	case "syntax-check":
		var all []Diagnostic
		for i, pb := range u.Playbooks {
			if i >= 50 {
				break
			}
			rel := strings.TrimPrefix(pb, u.Path+"/")
			out, err := s.Exec.Run(ctx, Cmd{Dir: dir, Argv: []string{bin, "--syntax-check", "-i", "localhost,", rel}, Env: s.ansEnv(), Timeout: 30 * time.Second})
			if err != nil {
				return nil, fail(err.Error())
			}
			if out.ExitCode != 0 {
				ds := ParseSyntaxCheck(append(out.Stderr, out.Stdout...), s.realRoot, u.Path, pb)
				if len(ds) == 0 {
					ds = []Diagnostic{{Tool: "syntax-check", Severity: SevError, Code: "syntax-check", Message: firstLine(append(out.Stderr, out.Stdout...)), File: pb, Line: 1, Col: 1}}
				}
				all = append(all, ds...)
			}
		}
		return all, ToolStatus{}
	}
	return nil, fail("unknown tool " + tool)
}

func lastNonWarning(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" && !strings.HasPrefix(l, "WARNING") && !strings.Contains(l, "UserWarning") {
			return l
		}
	}
	return "no output"
}

// validate runs `validate -json`; if terraform says the directory needs
// init, it runs `init -backend=false` in the private data dir and retries once.
func (s *Service) validate(ctx context.Context, u Unit, bin, _ string) ([]Diagnostic, ToolStatus) {
	env := s.tfEnv(u)
	defer s.lockUnit(u.ID)()
	if ctx.Err() != nil { // superseded while waiting for the previous run
		return nil, ToolStatus{}
	}
	dir, err := s.shadowDir(u)
	if err != nil {
		// Never fall back to validating in place: terraform init would write
		// .terraform.lock.hcl into the user's repo.
		return nil, fail("could not prepare a private working tree for validate: " + err.Error())
	}
	run := func() ([]Diagnostic, Output, error) {
		out, err := s.Exec.Run(ctx, Cmd{Dir: s.Root, Argv: []string{bin, "-chdir=" + dir, "validate", "-json", "-no-color"}, Env: env, Timeout: 2 * time.Minute})
		if err != nil {
			return nil, out, err
		}
		if len(strings.TrimSpace(string(out.Stdout))) == 0 {
			return nil, out, fmt.Errorf("no output (exit %d): %s", out.ExitCode, firstNonEmpty(firstLineOrEmpty(out.Stderr), "nothing on stderr either"))
		}
		ds, perr := ParseValidate(out.Stdout, u.Path)
		if perr != nil {
			return nil, out, perr
		}
		return ds, out, nil
	}
	// Try validate first: built-in providers and module-free roots need no init,
	// and a previously initialised data dir is reused. Only init when terraform
	// says it is required (or its output is unusable), then retry once.
	if ds, _, err := run(); err == nil && !initNeeded(ds) {
		return s.finishValidate(ds), ToolStatus{}
	}
	if st := s.initUnit(ctx, u, bin, dir, env); st != nil {
		return nil, *st
	}
	ds, out, err := run()
	if err != nil {
		return nil, fail("validate: " + firstNonEmpty(firstLineOrEmpty(out.Stderr), err.Error()))
	}
	if initNeeded(ds) { // still broken after init: surface it instead of hiding the cause
		return s.finishValidate(ds), ToolStatus{}
	}
	return s.finishValidate(ds), ToolStatus{}
}

func firstLineOrEmpty(b []byte) string {
	if len(strings.TrimSpace(string(b))) == 0 {
		return ""
	}
	return firstLine(b)
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

func (s *Service) initUnit(ctx context.Context, u Unit, bin, dir string, env []string) *ToolStatus {
	s.initMu.Lock() // one init at a time: concurrent inits corrupt a shared plugin cache
	defer s.initMu.Unlock()
	out, err := s.Exec.Run(ctx, Cmd{Dir: s.Root, Argv: []string{bin, "-chdir=" + dir, "init", "-backend=false", "-input=false", "-no-color"}, Env: env, Timeout: 5 * time.Minute})
	if err != nil {
		st := fail("init: " + err.Error())
		return &st
	}
	if out.ExitCode != 0 {
		st := fail("init failed: " + errorLine(out))
		st.Hint = "validate needs providers and modules; check your network or provider mirror, then re-run"
		return &st
	}
	return nil
}

func errorLine(o Output) string {
	for _, l := range strings.Split(string(o.Stderr)+"\n"+string(o.Stdout), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Error:") || strings.HasPrefix(l, "│ Error:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "│"))
		}
	}
	return firstLine(append(o.Stderr, o.Stdout...))
}

// finishValidate attaches terraform's own "Did you mean" suggestions as fixes.
func (s *Service) finishValidate(ds []Diagnostic) []Diagnostic {
	for i := range ds {
		if f := FixFromHint(ds[i]); f != nil {
			ds[i].Fix = f
		}
	}
	return ds
}
