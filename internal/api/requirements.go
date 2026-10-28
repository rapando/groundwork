package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rapando/groundwork/v2/internal/safego"
	"gopkg.in/yaml.v3"
)

// requirementsFiles are where an Ansible project lists the roles and
// collections it needs, relative to the project.
var requirementsFiles = []string{"requirements.yml", "requirements.yaml", "collections/requirements.yml", "collections/requirements.yaml", "roles/requirements.yml", "roles/requirements.yaml"}

// requirementsChanged schedules an install for each changed requirements
// file of a configured Ansible project.
func (a *API) requirementsChanged(paths []string) {
	cfg, _ := a.config()
	if cfg == nil || len(cfg.Ansible.Projects) == 0 || !cfg.Ansible.InstallRequirementsOn() {
		return
	}
	type req struct{ project, file string }
	var todo []req
	for _, p := range paths {
		for _, proj := range cfg.Ansible.Projects {
			base := path.Clean(proj.Path)
			rel := p
			if base != "." {
				if !strings.HasPrefix(p, base+"/") {
					continue
				}
				rel = strings.TrimPrefix(p, base+"/")
			}
			for _, f := range requirementsFiles {
				if rel == f {
					todo = append(todo, req{base, p})
				}
			}
		}
	}
	if len(todo) == 0 {
		return
	}
	a.prov.mu.Lock()
	defer a.prov.mu.Unlock()
	if a.prov.timers == nil {
		a.prov.timers, a.prov.tried = map[string]*time.Timer{}, map[string]string{}
	}
	for _, r := range todo {
		key := "req:" + r.file
		if t := a.prov.timers[key]; t != nil {
			t.Stop()
		}
		r := r
		a.prov.timers[key] = time.AfterFunc(providerSettle, func() {
			safego.Go(nil, "requirements "+r.file, a.InternalError, func() { a.installRequirements(r.project, r.file) })
		})
	}
}

// installRequirements runs ansible-galaxy for a requirements file whose
// content changed since the last install. A file that doesn't parse, or
// lists an entry without a name, is left for the next save.
func (a *API) installRequirements(project, file string) {
	ctx := a.prov.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	b, err := os.ReadFile(filepath.Join(a.Root, filepath.FromSlash(file)))
	if err != nil || !requirementsReady(b) {
		return
	}
	sum := sha256.Sum256(b)
	sig := hex.EncodeToString(sum[:])
	key := "req:" + file
	a.prov.mu.Lock()
	if a.prov.tried[key] == sig {
		a.prov.mu.Unlock()
		return
	}
	a.prov.tried[key] = sig
	a.prov.mu.Unlock()

	a.Bus.Publish("requirements.sync", map[string]any{"file": file, "status": "running"})
	if err := a.Runner.InstallRequirements(ctx, project, file); err != nil {
		a.Bus.Publish("requirements.sync", map[string]any{"file": file, "status": "failed", "error": err.Error()})
		return
	}
	a.Bus.Publish("requirements.sync", map[string]any{"file": file, "status": "done"})
}

// requirementsReady: the file parses, lists something, and every role and
// collection has a name (a plain string, or a name or src key).
func requirementsReady(b []byte) bool {
	var doc any
	if yaml.Unmarshal(b, &doc) != nil || doc == nil {
		return false
	}
	named := func(list any) (bool, int) {
		items, ok := list.([]any)
		if !ok {
			return list == nil, 0
		}
		for _, it := range items {
			switch v := it.(type) {
			case string:
				if strings.TrimSpace(v) == "" {
					return false, 0
				}
			case map[string]any:
				n, _ := v["name"].(string)
				s, _ := v["src"].(string)
				if strings.TrimSpace(n) == "" && strings.TrimSpace(s) == "" {
					return false, 0
				}
			default:
				return false, 0
			}
		}
		return true, len(items)
	}
	switch d := doc.(type) {
	case []any: // the old form: a list of roles
		ok, n := named(d)
		return ok && n > 0
	case map[string]any:
		okR, nr := named(d["roles"])
		okC, nc := named(d["collections"])
		return okR && okC && nr+nc > 0
	}
	return false
}
