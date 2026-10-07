package api

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/internal/ansible"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/vars"
)

// stack: the same Terraform configuration across environments. A root with
// workspace envs is one stack; env-per-directory roots (envs/dev, envs/prod)
// group by their path with the env segment wildcarded (envs/*).
type stack struct {
	ID      string        `json:"id"`
	Targets []stackTarget `json:"targets"`
}

type stackTarget struct {
	Root string `json:"root"`
	Env  string `json:"env"`
}

func stacksFor(cfg *config.Config) []stack {
	if cfg == nil {
		return nil
	}
	byID := map[string]*stack{}
	var order []string
	add := func(id, root, env string) {
		if byID[id] == nil {
			byID[id] = &stack{ID: id}
			order = append(order, id)
		}
		byID[id].Targets = append(byID[id].Targets, stackTarget{root, env})
	}
	for _, r := range cfg.Terraform.Roots {
		p := path.Clean(r.Path)
		if r.Env != "" {
			segs := strings.Split(p, "/")
			id := p
			for i, s := range segs {
				if s == r.Env {
					segs[i] = "*"
					id = strings.Join(segs, "/")
					break
				}
			}
			add(id, p, r.Env)
			continue
		}
		envs := make([]string, 0, len(r.Envs))
		for e := range r.Envs {
			envs = append(envs, e)
		}
		sort.Slice(envs, func(i, j int) bool {
			if envRank(envs[i]) != envRank(envs[j]) {
				return envRank(envs[i]) < envRank(envs[j])
			}
			return envs[i] < envs[j]
		})
		for _, e := range envs {
			add(p, p, e)
		}
	}
	out := make([]stack, 0, len(order))
	for _, id := range order {
		s := byID[id]
		sort.SliceStable(s.Targets, func(i, j int) bool { return envRank(s.Targets[i].Env) < envRank(s.Targets[j].Env) })
		out = append(out, *s)
	}
	return out
}

func (a *API) varTargets(cfg *config.Config, st stack) []vars.Target {
	var ts []vars.Target
	for _, t := range st.Targets {
		rt, err := runner.ResolveTarget(cfg, t.Root, t.Env)
		if err != nil {
			continue
		}
		ts = append(ts, vars.Target{Root: rt.Root, Env: rt.Env, VarFiles: rt.VarFiles})
	}
	return ts
}

func (a *API) terraformVars(w http.ResponseWriter, r *http.Request) {
	cfg, _ := a.config()
	stacks := stacksFor(cfg)
	resp := map[string]any{"stacks": stacks, "rows": []vars.Row{}}
	if len(stacks) == 0 {
		writeJSON(w, 200, resp)
		return
	}
	sel := stacks[0]
	if q := r.URL.Query().Get("stack"); q != "" {
		i := slices.IndexFunc(stacks, func(s stack) bool { return s.ID == q })
		if i < 0 {
			writeError(w, 404, "not_found", "no stack "+q, nil)
			return
		}
		sel = stacks[i]
	}
	resp["stack"] = sel.ID
	resp["rows"] = vars.Matrix(a.Root, a.varTargets(cfg, sel), os.Environ())
	writeJSON(w, 200, resp)
}

type setVarReq struct {
	Root  string `json:"root"`
	Env   string `json:"env"`
	Name  string `json:"name"`
	Value string `json:"value"` // HCL literal as typed
	File  string `json:"file"`  // one of the cell's writable files
	Apply bool   `json:"apply"`
	SHA   string `json:"sha"` // from the preview ("" when the file doesn't exist yet)
}

// setTerraformVar previews or writes one variable value into a tfvars file.
func (a *API) setTerraformVar(w http.ResponseWriter, r *http.Request) {
	var req setVarReq
	if !decode(w, r, &req) {
		return
	}
	cfg, _ := a.config()
	rt, err := runner.ResolveTarget(cfg, req.Root, req.Env)
	if err != nil {
		writeError(w, 400, "bad_target", err.Error(), nil)
		return
	}
	t := vars.Target{Root: rt.Root, Env: rt.Env, VarFiles: rt.VarFiles}
	d := vars.Declarations(a.Root, filepath.Join(a.Root, filepath.FromSlash(t.Root)))[req.Name]
	if d == nil {
		writeError(w, 404, "not_found", req.Name+" isn't declared in "+t.Root, nil)
		return
	}
	if d.Sensitive {
		writeError(w, 422, "sensitive", req.Name+" is sensitive: don't write it to a tfvars file in plain text. Set TF_VAR_"+req.Name+" in groundwork's environment, or keep it in SOPS or a secrets manager.", nil)
		return
	}
	cell := vars.Resolve(a.Root, t, d, nil)
	if !slices.Contains(cell.Writable, req.File) {
		writeError(w, 400, "bad_file", "write to one of: "+strings.Join(cell.Writable, ", "), nil)
		return
	}
	v, err := vars.ParseLiteral(req.Value)
	if err != nil {
		writeError(w, 422, "bad_value", err.Error(), nil)
		return
	}
	var cur string
	sha := ""
	c, err := files.Read(a.Root, req.File)
	switch {
	case err == nil:
		cur, sha = c.Text, c.SHA
	case errors.Is(err, files.ErrNotFound):
	default:
		fileError(w, err)
		return
	}
	next, err := vars.SetValue([]byte(cur), req.File, req.Name, v)
	if err != nil {
		writeError(w, 422, "edit_failed", err.Error(), nil)
		return
	}
	diff := files.UnifiedDiff(req.File, cur, string(next))
	if !req.Apply {
		writeJSON(w, 200, map[string]any{"file": req.File, "diff": diff, "sha": sha, "create": sha == ""})
		return
	}
	if req.SHA != sha {
		writeError(w, 409, "conflict", "the file changed since the preview; preview again", nil)
		return
	}
	newSHA, err := files.Write(a.Root, req.File, next, sha, sha == "")
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{req.File}) // validates the root with the new value's file
	writeJSON(w, 200, map[string]any{"file": req.File, "sha": newSHA, "written": true})
}

func (a *API) projectInventories(p config.AnsibleProject) map[string]string {
	dir := filepath.Join(a.Root, filepath.FromSlash(p.Path))
	invs := map[string]string{}
	for env, inv := range p.Inventories {
		invs[env] = filepath.Join(dir, filepath.FromSlash(inv))
	}
	if len(invs) == 0 {
		cfgPath := filepath.Join(dir, firstNonEmptyS(p.Config, "ansible.cfg"))
		if inv := ansible.DefaultInventory(cfgPath); inv != "" {
			invs["default"] = filepath.Join(dir, inv)
		}
	}
	return invs
}

func firstNonEmptyS(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a *API) ansibleVars(w http.ResponseWriter, r *http.Request) {
	cfg, _ := a.config()
	resp := map[string]any{"projects": []string{}}
	if cfg == nil || len(cfg.Ansible.Projects) == 0 {
		writeJSON(w, 200, resp)
		return
	}
	var names []string
	for _, p := range cfg.Ansible.Projects {
		names = append(names, path.Clean(p.Path))
	}
	resp["projects"] = names
	proj := cfg.Ansible.Projects[0]
	if q := r.URL.Query().Get("project"); q != "" {
		i := slices.IndexFunc(cfg.Ansible.Projects, func(p config.AnsibleProject) bool { return path.Clean(p.Path) == path.Clean(q) })
		if i < 0 {
			writeError(w, 404, "not_found", "no Ansible project "+q, nil)
			return
		}
		proj = cfg.Ansible.Projects[i]
	}
	resp["project"] = path.Clean(proj.Path)
	resp["matrix"] = ansible.GroupVarsMatrix(a.Root, filepath.Join(a.Root, filepath.FromSlash(proj.Path)), a.projectInventories(proj))
	writeJSON(w, 200, resp)
}

// ---- secrets ----

var secretsMu sync.Mutex

func (a *API) secretStore() *vars.Secrets {
	secretsMu.Lock()
	if a.secrets == nil {
		a.secrets = vars.NewSecrets(a.Root, nil)
	}
	s := a.secrets
	secretsMu.Unlock()
	cfg, _ := a.config()
	var ps []vars.VaultProject
	if cfg != nil {
		for _, p := range cfg.Ansible.Projects {
			dir := filepath.Join(a.Root, filepath.FromSlash(p.Path))
			vp := vars.VaultProject{Dir: dir}
			if c := filepath.Join(dir, firstNonEmptyS(p.Config, "ansible.cfg")); fileExists(c) {
				vp.Config = c
			}
			if pw := p.VaultPasswordFile; pw != "" {
				if strings.HasPrefix(pw, "~/") {
					if home, err := os.UserHomeDir(); err == nil {
						pw = filepath.Join(home, pw[2:])
					}
				} else if !filepath.IsAbs(pw) {
					pw = filepath.Join(dir, pw)
				}
				vp.PasswordFile = pw
			}
			ps = append(ps, vp)
		}
	}
	s.SetProjects(ps)
	return s
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func (a *API) listSecrets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	list := a.secretStore().List(ctx, r.URL.Query().Get("check") != "0")
	if list == nil {
		list = []vars.Secret{}
	}
	reveals, _ := a.Store.ListReveals(20)
	noStore(w)
	writeJSON(w, 200, map[string]any{"secrets": list, "reveals": reveals})
}

type revealReq struct {
	ID   string `json:"id"`
	Root string `json:"root"` // Terraform sensitive variable: root + env + name
	Env  string `json:"env"`
	Name string `json:"name"`
}

// revealSecret decrypts one value. The value goes into this response only:
// not the log, not the database (the audit row holds the name), not the cache.
func (a *API) revealSecret(w http.ResponseWriter, r *http.Request) {
	var req revealReq
	if !decode(w, r, &req) {
		return
	}
	noStore(w)
	if req.ID == "" {
		cfg, _ := a.config()
		rt, err := runner.ResolveTarget(cfg, req.Root, req.Env)
		if err != nil {
			writeError(w, 400, "bad_target", err.Error(), nil)
			return
		}
		d := vars.Declarations(a.Root, filepath.Join(a.Root, filepath.FromSlash(rt.Root)))[req.Name]
		if d == nil {
			writeError(w, 404, "not_found", req.Name+" isn't declared in "+rt.Root, nil)
			return
		}
		v, err := vars.RawValue(a.Root, vars.Target{Root: rt.Root, Env: rt.Env, VarFiles: rt.VarFiles}, d)
		if err != nil {
			writeError(w, 422, "no_value", err.Error(), nil)
			return
		}
		_ = a.Store.RecordReveal("terraform", req.Name, rt.Root+" ("+rt.Env+")")
		writeJSON(w, 200, map[string]any{"value": v})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	sec, v, err := a.secretStore().Reveal(ctx, req.ID)
	if err != nil {
		code := 500
		if errors.Is(err, vars.ErrNotRevealable) {
			code = 422
		}
		writeError(w, code, "reveal_failed", err.Error(), nil)
		return
	}
	_ = a.Store.RecordReveal(sec.Kind, sec.Name, sec.File)
	writeJSON(w, 200, map[string]any{"value": v})
}

func (a *API) scanSecrets(w http.ResponseWriter, r *http.Request) {
	findings := []vars.Finding{}
	_ = filepath.WalkDir(a.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || r.Context().Err() != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".groundwork", ".terraform", "node_modules", ".venv", "venv":
				return filepath.SkipDir
			}
			return nil
		}
		if !vars.Scannable(p) {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 1<<20 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(a.Root, p)
		findings = append(findings, vars.Scan(filepath.ToSlash(rel), b)...)
		return nil
	})
	writeJSON(w, 200, map[string]any{"findings": findings})
}

type moveReq struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Apply bool   `json:"apply"`
	SHA   string `json:"sha"`
}

// moveSecret encrypts a plaintext value in an Ansible vars file in place
// (ansible-vault encrypt_string). The preview masks the old value.
func (a *API) moveSecret(w http.ResponseWriter, r *http.Request) {
	var req moveReq
	if !decode(w, r, &req) {
		return
	}
	ext := path.Ext(req.File)
	if ext != ".yml" && ext != ".yaml" {
		writeError(w, 422, "unsupported", "only Ansible YAML vars files can be moved to ansible-vault here. For Terraform, mark the variable sensitive and pass it with TF_VAR_<name> or keep it in SOPS (see docs/variables.md).", nil)
		return
	}
	cur, err := files.Read(a.Root, req.File)
	if err != nil {
		fileError(w, err)
		return
	}
	key, _, err := vars.PlainAssignment([]byte(cur.Text), req.Line)
	if err != nil {
		writeError(w, 422, "unsupported", err.Error(), nil)
		return
	}
	masked := vars.ReplaceLine([]byte(cur.Text), req.Line, key+": ••••••")
	placeholder := vars.ReplaceLine([]byte(cur.Text), req.Line, key+": !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  …encrypted when you apply…")
	if !req.Apply {
		writeJSON(w, 200, map[string]any{"file": req.File, "key": key, "diff": files.UnifiedDiff(req.File, string(masked), string(placeholder)), "sha": cur.SHA})
		return
	}
	if req.SHA != cur.SHA {
		writeError(w, 409, "conflict", "the file changed since the preview; preview again", nil)
		return
	}
	_, val, _ := vars.PlainAssignment([]byte(cur.Text), req.Line)
	abs := filepath.Join(a.Root, filepath.FromSlash(req.File))
	block, err := a.secretStore().EncryptString(r.Context(), abs, key, val)
	if err != nil {
		writeError(w, 422, "encrypt_failed", err.Error(), nil)
		return
	}
	sha, err := files.Write(a.Root, req.File, vars.ReplaceLine([]byte(cur.Text), req.Line, block), cur.SHA, false)
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{req.File})
	writeJSON(w, 200, map[string]any{"file": req.File, "sha": sha, "written": true,
		"note": "The old value is still in git history: rotate it."})
}
