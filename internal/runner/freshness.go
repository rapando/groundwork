package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rapando/groundwork/internal/terraform"
)

// StateRef identifies the state a plan was made against.
type StateRef struct {
	Exists  bool   `json:"exists"`
	Serial  int64  `json:"serial"`
	Lineage string `json:"lineage,omitempty"`
}

// StaleError lists why a saved plan may no longer be applied.
type StaleError struct{ Reasons []string }

func (e *StaleError) Error() string { return "re-plan required: " + strings.Join(e.Reasons, "; ") }

// ErrStale matches *StaleError with errors.Is.
var ErrStale = errors.New("plan is stale")

func (e *StaleError) Is(target error) bool { return target == ErrStale }

// readState runs `terraform state pull` (read-only, takes no lock). The
// workspace is passed via TF_WORKSPACE so a concurrent job's `workspace
// select` cannot change what is read.
func (r *Runner) readState(ctx context.Context, t Target) (StateRef, error) {
	bin, err := exec.LookPath(t.Binary)
	if err != nil {
		return StateRef{}, fmt.Errorf("%s not found on PATH", t.Binary)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, terraform.StateSerialArgs(t.Binary, r.abs(t.Root))[1:]...)
	cmd.Dir = r.Root
	cmd.Env = append(os.Environ(), tfEnv...)
	if t.Workspace != "" {
		cmd.Env = append(cmd.Env, "TF_WORKSPACE="+t.Workspace)
	}
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(ee.Stderr)), "\n", 2)[0])
		}
		return StateRef{}, fmt.Errorf("terraform state pull: %s", msg)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return StateRef{}, nil // no state yet
	}
	var s struct {
		Serial  int64  `json:"serial"`
		Lineage string `json:"lineage"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return StateRef{}, fmt.Errorf("terraform state pull: unexpected output: %w", err)
	}
	return StateRef{Exists: true, Serial: s.Serial, Lineage: s.Lineage}, nil
}

// fingerprint hashes everything the plan was computed from: the root's files,
// the local modules it uses (from .terraform/modules/modules.json), and its
// var files. State files and terraform's working data are excluded: state
// changes are detected by serial, not by content.
func (r *Runner) fingerprint(t Target) (string, error) {
	h := sha256.New()
	var dirs []string
	if t.isAnsible() {
		dirs = []string{r.abs(t.Project)}
	} else {
		dirs = terraform.ModuleDirs(r.abs(t.Root))
	}
	var files []string
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				if p != d && (de.Name() == ".terraform" || de.Name() == ".git" || de.Name() == ".groundwork" || de.Name() == ".ansible" || de.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			n := de.Name()
			if strings.HasPrefix(n, "terraform.tfstate") || n == ".terraform.tfstate.lock.info" || strings.HasSuffix(n, ".tfstate") || strings.HasSuffix(n, ".tfstate.backup") {
				return nil
			}
			if de.Type().IsRegular() {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	for _, vf := range t.VarFiles {
		files = append(files, r.abs(vf))
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(h, "%s\x00missing\x00", f)
			continue
		}
		rel, _ := filepath.Rel(r.Root, f)
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// staleness re-checks a waiting plan. checkState reads the live state, which
// can take a moment on remote backends.
func (r *Runner) staleness(ctx context.Context, t Target, s Summary, checkState bool) ([]string, StateRef, error) {
	var reasons []string
	ttl := time.Hour
	if cfg := r.Cfg(); cfg != nil {
		ttl = cfg.Terraform.PlanTTLDuration()
	}
	if s.PlannedAt != nil && time.Since(*s.PlannedAt) > ttl {
		reasons = append(reasons, fmt.Sprintf("the plan is older than %s (made %s ago)", ttl, time.Since(*s.PlannedAt).Round(time.Minute)))
	}
	if s.Fingerprint != "" {
		fp, err := r.fingerprint(t)
		if err != nil {
			return nil, StateRef{}, err
		}
		if fp != s.Fingerprint {
			reasons = append(reasons, "the configuration changed since the plan (files in "+t.Root+", its modules or var files)")
		}
	}
	var cur StateRef
	if checkState && s.State != nil {
		var err error
		cur, err = r.readState(ctx, t)
		if err != nil {
			return nil, StateRef{}, err
		}
		was := *s.State
		switch {
		case was.Exists && !cur.Exists:
			reasons = append(reasons, "the state was removed since the plan")
		case !was.Exists && cur.Exists:
			reasons = append(reasons, fmt.Sprintf("state was created since the plan (serial %d)", cur.Serial))
		case was.Exists && cur.Lineage != was.Lineage:
			reasons = append(reasons, "the state was replaced since the plan (different lineage)")
		case was.Exists && cur.Serial != was.Serial:
			reasons = append(reasons, fmt.Sprintf("the state changed since the plan (serial %d → %d)", was.Serial, cur.Serial))
		}
	}
	return reasons, cur, nil
}
