package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rapando/groundwork/v2/internal/proc"
)

// InstallRequirements installs an Ansible project's roles and collections
// from a requirements file (`ansible-galaxy install -r`), into the same
// ANSIBLE_HOME groundwork's playbook runs, syntax checks and ansible-lint
// use, so what it installs is what they find. It runs in the project
// directory, so collections_path and roles_path in its ansible.cfg apply.
// project and file are repo-relative; file is relative to the repository.
func (r *Runner) InstallRequirements(ctx context.Context, project, file string) error {
	bin, err := exec.LookPath("ansible-galaxy")
	if err != nil {
		return fmt.Errorf("ansible-galaxy not found on PATH")
	}
	home := filepath.Join(r.Root, ".groundwork", "ansible-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "install", "-r", r.abs(file))
	proc.GroupContext(cmd)
	cmd.Dir = r.abs(project)
	cmd.Env = append(os.Environ(), "ANSIBLE_HOME="+home, "ANSIBLE_NOCOLOR=1", "ANSIBLE_FORCE_COLOR=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ansible-galaxy install failed: %s", galaxyError(out))
	}
	return nil
}

// galaxyError is the line of ansible-galaxy's output that says what went wrong.
func galaxyError(out []byte) string {
	lines := splitLines(out)
	for _, l := range lines {
		if len(l) >= 6 && l[:6] == "ERROR!" {
			return l
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1]
	}
	return "no output"
}

func splitLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
