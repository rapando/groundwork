package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rapando/groundwork/v2/internal/proc"
	"github.com/rapando/groundwork/v2/internal/terraform"
)

// ErrRootBusy: a run holds the root, and its own init installs what it needs.
var ErrRootBusy = errors.New("a run is using this root")

// ProviderSync is what SyncProviders changed.
type ProviderSync struct {
	Root    string   `json:"root"`
	Removed []string `json:"removed,omitempty"` // addresses deleted from the root's .terraform
}

// SyncProviders installs the providers a root's configuration requires and
// removes the ones it no longer does: `init -backend=false` (which downloads
// them and updates .terraform.lock.hcl), then the providers the lock file no
// longer lists are deleted from the root's .terraform. It never waits for a
// run on the root: it returns ErrRootBusy instead.
func (r *Runner) SyncProviders(ctx context.Context, root, binary string) (ProviderSync, error) {
	res := ProviderSync{Root: root}
	unlock, ok := r.tryLock("tf:" + root)
	if !ok {
		return res, ErrRootBusy
	}
	defer unlock()
	bin, err := exec.LookPath(binary)
	if err != nil {
		return res, fmt.Errorf("%s not found on PATH", binary)
	}
	dir := r.abs(root)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-chdir="+dir, "init", "-backend=false", "-input=false", "-no-color")
	proc.GroupContext(cmd)
	cmd.Dir = r.Root
	cmd.Env = append(os.Environ(), tfEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, fmt.Errorf("%s init failed: %s", binary, initError(out))
	}
	locked, err := terraform.LockedProviders(dir)
	if err != nil {
		return res, err
	}
	data := os.Getenv("TF_DATA_DIR")
	switch {
	case data == "":
		data = filepath.Join(dir, ".terraform")
	case !filepath.IsAbs(data):
		data = filepath.Join(dir, data)
	}
	res.Removed, err = terraform.PruneProviders(data, locked)
	return res, err
}

// tryLock takes a root's run lock if nobody holds it.
func (r *Runner) tryLock(key string) (func(), bool) {
	r.mu.Lock()
	ch, ok := r.locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		r.locks[key] = ch
	}
	r.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	default:
		return nil, false
	}
}

// initError is the line of init's output that says what went wrong.
func initError(out []byte) string {
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(strings.TrimLeft(l, "│╷╵ "))
		if strings.HasPrefix(l, "Error:") {
			return l
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "no output"
}
