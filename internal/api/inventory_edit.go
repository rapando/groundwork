package api

import (
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rapando/groundwork/internal/ansible"
	"github.com/rapando/groundwork/internal/files"
)

// Structured inventory edits: add/remove hosts and groups and set/unset
// variables, previewed as a diff and written only on apply (with the
// preview's SHA, like Terraform variable edits).

type invEditReq struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	Op      string `json:"op"`
	Host    string `json:"host"`
	Group   string `json:"group"`
	Parent  string `json:"parent"`
	Address string `json:"address"`
	Scope   string `json:"scope"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	File    string `json:"file"` // set_var/unset_var: one of the var targets
	Apply   bool   `json:"apply"`
	SHA     string `json:"sha"`
}

type varTarget struct {
	File   string `json:"file"`   // repo-relative
	Kind   string `json:"kind"`   // inventory | group_vars | host_vars
	Exists bool   `json:"exists"` // false: written as a new file
}

func (a *API) findScope(project, env string) (invScope, error) {
	cfg, _ := a.config()
	for _, s := range scopesFor(cfg) {
		if (project == "" || s.Project == path.Clean(project)) && s.Env == env {
			return s, nil
		}
	}
	return invScope{}, errors.New("unknown project/environment")
}

func (a *API) rel(p string) string {
	r, err := filepath.Rel(a.Root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// editableInventory returns the scope's inventory file when groundwork can
// edit it (a static YAML file in the repo), else why not.
func (a *API) editableInventory(s invScope) (string, string) {
	f := a.inventoryFile(s)
	if f == "" {
		return "", "this environment has no inventory file (ansible.cfg default)"
	}
	st, err := os.Stat(f)
	switch {
	case err != nil:
		return "", "the inventory file doesn't exist"
	case st.IsDir():
		return "", "the inventory is a directory: edit its files in Code"
	}
	if ext := strings.ToLower(filepath.Ext(f)); ext != ".yml" && ext != ".yaml" {
		return "", "only YAML inventories can be edited here: edit " + a.rel(f) + " in Code"
	}
	rel := a.rel(f)
	if strings.HasPrefix(rel, "../") {
		return "", "the inventory is outside the repository"
	}
	if b, err := os.ReadFile(f); err == nil && strings.HasPrefix(strings.TrimSpace(string(b)), "$ANSIBLE_VAULT;") {
		return "", "the inventory file is vault-encrypted"
	}
	return rel, ""
}

// inventoryVarTargets are the files a host or group variable can be written to: the
// inventory itself, existing readable group_vars/host_vars files (inventory
// directory first, then the playbook directory), or a new file next to the
// inventory when there's none.
func (a *API) inventoryVarTargets(s invScope, scope, name string) []varTarget {
	out := []varTarget{}
	if inv, _ := a.editableInventory(s); inv != "" {
		out = append(out, varTarget{File: inv, Kind: "inventory", Exists: true})
	}
	kind := map[string]string{"host": "host_vars", "group": "group_vars"}[scope]
	if kind == "" || (scope == "host" && !ansible.ValidHostName(name)) || (scope == "group" && !ansible.ValidGroupName(name)) {
		return out
	}
	projectDir := filepath.Join(a.Root, filepath.FromSlash(s.Project))
	invDir := projectDir
	if f := a.inventoryFile(s); f != "" {
		if st, err := os.Stat(f); err == nil && st.IsDir() {
			invDir = f
		} else {
			invDir = filepath.Dir(f)
		}
	}
	seen := map[string]bool{}
	invHasOwn := false
	for _, dir := range []string{invDir, projectDir} {
		plain, _ := ansible.VarsFiles(dir, kind, name)
		for _, f := range plain {
			if r := a.rel(f); !seen[r] && !strings.HasPrefix(r, "../") {
				seen[r] = true
				out = append(out, varTarget{File: r, Kind: kind, Exists: true})
				invHasOwn = invHasOwn || dir == invDir
			}
		}
	}
	if !invHasOwn {
		// a group_vars/<name>/ directory takes main.yml; otherwise <name>.yml
		base := filepath.Join(invDir, kind, name)
		f := base + ".yml"
		if st, err := os.Stat(base); err == nil && st.IsDir() {
			f = filepath.Join(base, "main.yml")
		}
		if r := a.rel(f); !seen[r] && !strings.HasPrefix(r, "../") {
			out = append(out, varTarget{File: r, Kind: kind, Exists: false})
		}
	}
	return out
}

func (a *API) getVarTargets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s, err := a.findScope(q.Get("project"), q.Get("env"))
	if err != nil {
		writeError(w, 400, "bad_request", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"targets": a.inventoryVarTargets(s, q.Get("scope"), q.Get("name"))})
}

func (a *API) editInventory(w http.ResponseWriter, r *http.Request) {
	var req invEditReq
	if !decode(w, r, &req) {
		return
	}
	s, err := a.findScope(req.Project, req.Env)
	if err != nil {
		writeError(w, 400, "bad_request", err.Error(), nil)
		return
	}
	inv, why := a.editableInventory(s)
	isVar := req.Op == "set_var" || req.Op == "unset_var"
	if isVar && req.Op == "set_var" && ansible.IsSecretKey(req.Key) {
		writeError(w, 422, "secret", req.Key+" looks like a secret: don't write it in plain text. Keep it in ansible-vault (Variables → Secrets can move a value there).", nil)
		return
	}
	if isVar && req.Scope != "host" && req.Scope != "group" {
		writeError(w, 400, "bad_request", "scope must be host or group", nil)
		return
	}

	file := inv
	create := false
	if isVar {
		name := req.Host
		if req.Scope == "group" {
			name = req.Group
		}
		targets := a.inventoryVarTargets(s, req.Scope, name)
		i := slices.IndexFunc(targets, func(t varTarget) bool { return t.File == req.File })
		if i < 0 {
			var names []string
			for _, t := range targets {
				names = append(names, t.File)
			}
			writeError(w, 400, "bad_file", "write to one of: "+strings.Join(names, ", "), nil)
			return
		}
		file, create = targets[i].File, !targets[i].Exists
	}
	if file == "" {
		writeError(w, 422, "not_editable", why, nil)
		return
	}

	var cur string
	sha := ""
	switch c, err := files.Read(a.Root, file); {
	case err == nil:
		cur, sha, create = c.Text, c.SHA, false
	case errors.Is(err, files.ErrNotFound) && create:
	default:
		fileError(w, err)
		return
	}

	var next []byte
	if file == inv {
		next, err = ansible.EditInventory([]byte(cur), ansible.InvEdit{
			Op: req.Op, Host: req.Host, Group: req.Group, Parent: req.Parent, Address: req.Address,
			Scope: req.Scope, Key: req.Key, Value: req.Value,
		})
	} else {
		var v *string
		if req.Op == "set_var" {
			v = &req.Value
		}
		next, err = ansible.EditVarsFile([]byte(cur), req.Key, v)
	}
	if err != nil {
		writeError(w, 422, "edit_failed", err.Error(), nil)
		return
	}
	diff := files.UnifiedDiff(file, cur, string(next))
	if !req.Apply {
		writeJSON(w, 200, map[string]any{"file": file, "diff": diff, "sha": sha, "create": sha == ""})
		return
	}
	if req.SHA != sha {
		writeError(w, 409, "conflict", "the file changed since the preview; preview again", nil)
		return
	}
	newSHA, err := files.Write(a.Root, file, next, sha, sha == "")
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{file})
	writeJSON(w, 200, map[string]any{"file": file, "sha": newSHA, "written": true})
}
