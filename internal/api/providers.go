package api

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/v2/internal/checks"
	"github.com/rapando/groundwork/v2/internal/runner"
	"github.com/rapando/groundwork/v2/internal/safego"
	"github.com/rapando/groundwork/v2/internal/terraform"
)

// providerSettle: how long a root's .tf files must stay unchanged before its
// providers are installed. Autosave writes every second while you type, and
// a version typed one digit at a time is valid at every step.
var providerSettle = 3 * time.Second

type providerWatch struct {
	ctx    context.Context // the API's; nil before Start
	mu     sync.Mutex
	timers map[string]*time.Timer // root → pending sync
	tried  map[string]string      // root → the requirements last synced for
}

// providersChanged schedules a provider sync for each Terraform root whose
// files, or local modules, changed.
func (a *API) providersChanged(paths []string) {
	cfg, _ := a.config()
	if cfg == nil || len(cfg.Terraform.Roots) == 0 || !cfg.Terraform.InstallProvidersOn() {
		return
	}
	var units []checks.Unit
	roots := map[string]checks.Unit{}
	for _, p := range paths {
		if !strings.HasSuffix(p, ".tf") || strings.Contains("/"+p, "/.terraform/") || strings.HasPrefix(p, ".groundwork/") {
			continue
		}
		if units == nil {
			units = a.Checks.Units()
		}
		for _, u := range checks.Affected(units, p) {
			if u.Kind == checks.KindRoot {
				roots[u.Path] = u
			}
		}
	}
	a.prov.mu.Lock()
	defer a.prov.mu.Unlock()
	if a.prov.timers == nil {
		a.prov.timers, a.prov.tried = map[string]*time.Timer{}, map[string]string{}
	}
	for root, u := range roots {
		if t := a.prov.timers[root]; t != nil {
			t.Stop()
		}
		u := u
		a.prov.timers[root] = time.AfterFunc(providerSettle, func() {
			safego.Go(nil, "providers "+u.Path, a.InternalError, func() { a.syncProviders(u) })
		})
	}
}

// syncProviders installs or removes a root's providers if its
// required_providers no longer match its lock file. It waits, doing nothing,
// while any entry lacks a source or a version, or a file doesn't parse.
func (a *API) syncProviders(u checks.Unit) {
	ctx := a.prov.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	var reqs []terraform.Requirement
	for _, d := range append([]string{u.Path}, u.Deps...) {
		rs, err := terraform.RequiredProviders(filepath.Join(a.Root, filepath.FromSlash(d)))
		if err != nil {
			return // mid-edit: the next save tries again
		}
		reqs = append(reqs, rs...)
	}
	for _, r := range reqs {
		if !r.Complete() {
			return
		}
	}
	locked, err := terraform.LockedProviders(filepath.Join(a.Root, filepath.FromSlash(u.Path)))
	if err != nil || providersInSync(reqs, locked) {
		return
	}
	sig := requirementsKey(reqs)
	a.prov.mu.Lock()
	if a.prov.tried[u.Path] == sig { // already done, or failed: wait for a different edit
		a.prov.mu.Unlock()
		return
	}
	a.prov.tried[u.Path] = sig
	a.prov.mu.Unlock()

	cfg, _ := a.config()
	bin := "terraform"
	if cfg != nil && cfg.Terraform.Binary != "" {
		bin = cfg.Terraform.Binary
	}
	a.Bus.Publish("providers.sync", map[string]any{"root": u.Path, "status": "running"})
	res, err := a.Runner.SyncProviders(ctx, u.Path, bin)
	switch {
	case errors.Is(err, runner.ErrRootBusy):
		a.forgetProviders(u.Path) // the run's own init installs them; try again on the next edit
		a.Bus.Publish("providers.sync", map[string]any{"root": u.Path, "status": "skipped"})
	case err != nil:
		a.Bus.Publish("providers.sync", map[string]any{"root": u.Path, "status": "failed", "error": err.Error()})
	default:
		a.Bus.Publish("providers.sync", map[string]any{"root": u.Path, "status": "done", "removed": res.Removed})
	}
}

func (a *API) forgetProviders(root string) {
	a.prov.mu.Lock()
	delete(a.prov.tried, root)
	a.prov.mu.Unlock()
}

// providersInSync: every requirement is locked with the same constraint, and
// the lock file holds nothing else.
func providersInSync(reqs []terraform.Requirement, locked []terraform.LockedProvider) bool {
	used := make([]bool, len(locked))
	for _, r := range reqs {
		found := false
		for i, l := range locked {
			if terraform.SameProvider(r.Source, l.Address) && squash(l.Constraints) == squash(r.Version) {
				used[i], found = true, true
			}
		}
		if !found {
			return false
		}
	}
	for _, u := range used {
		if !u {
			return false
		}
	}
	return true
}

func squash(s string) string { return strings.Join(strings.Fields(s), "") }

func requirementsKey(reqs []terraform.Requirement) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = strings.ToLower(r.Source) + "@" + squash(r.Version)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
