package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/workspace"
)

// Section is a managed area of the repo shown as a group in the file tree.
type Section struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // terraform | ansible
	Base string `json:"base"`
}

func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := strings.Split(paths[0], "/")
	for _, p := range paths[1:] {
		segs := strings.Split(p, "/")
		n := 0
		for n < len(common) && n < len(segs) && common[n] == segs[n] {
			n++
		}
		common = common[:n]
	}
	if len(common) == 0 {
		return "."
	}
	return strings.Join(common, "/")
}

func sectionsFor(cfg *config.Config) []Section {
	if cfg == nil {
		return nil
	}
	var secs []Section
	var tf []string
	for _, r := range cfg.Terraform.Roots {
		tf = append(tf, path.Clean(r.Path))
	}
	for _, m := range cfg.Terraform.Modules {
		tf = append(tf, path.Clean(strings.TrimSuffix(m, "/*")))
	}
	if len(tf) > 0 {
		secs = append(secs, Section{Name: "Terraform", Kind: "terraform", Base: commonDir(tf)})
	}
	for _, p := range cfg.Ansible.Projects {
		name := "Ansible"
		if len(cfg.Ansible.Projects) > 1 {
			name += " · " + p.Path
		}
		secs = append(secs, Section{Name: name, Kind: "ansible", Base: path.Clean(p.Path)})
	}
	return secs
}

var rootConfigFiles = map[string]bool{
	config.FileName: true, ".tflint.hcl": true, ".yamllint": true, ".ansible-lint": true, ".pre-commit-config.yaml": true,
}

func looksLikeIaC(f string) bool {
	switch path.Ext(f) {
	case ".tf", ".tfvars", ".hcl", ".yml", ".yaml":
		return true
	}
	b := path.Base(f)
	return b == "ansible.cfg" || strings.HasSuffix(f, ".tf.json") || strings.HasSuffix(f, ".tfvars.json")
}

func under(base, f string) bool { return base == "." || f == base || strings.HasPrefix(f, base+"/") }

func (a *API) listFiles(w http.ResponseWriter, r *http.Request) {
	cfg, _ := a.config()
	var ignore []string
	if cfg != nil {
		ignore = cfg.Ignore
	}
	tree, err := workspace.ListTree(a.Root, ignore)
	if err != nil {
		writeError(w, 500, "list_failed", err.Error(), nil)
		return
	}
	secs := sectionsFor(cfg)
	out := tree.Files
	if r.URL.Query().Get("iac_only") == "1" {
		out = make([]string, 0, len(tree.Files))
		for _, f := range tree.Files {
			keep := false
			switch {
			case cfg == nil:
				keep = looksLikeIaC(f)
			case rootConfigFiles[f]:
				keep = true
			default:
				for _, s := range secs {
					if under(s.Base, f) {
						keep = true
						break
					}
				}
			}
			if keep {
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	writeJSON(w, 200, map[string]any{"files": out, "sections": secs, "truncated": tree.Truncated, "total": len(tree.Files)})
}

func (a *API) readFile(w http.ResponseWriter, r *http.Request) {
	c, err := files.Read(a.Root, r.URL.Query().Get("path"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

type writeReq struct {
	Content string `json:"content"`
	Create  bool   `json:"create"`
}

func (a *API) writeFile(w http.ResponseWriter, r *http.Request) {
	var req writeReq
	r.Body = http.MaxBytesReader(w, r.Body, files.MaxEditSize+4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON body: "+err.Error(), nil)
		return
	}
	ifMatch := strings.Trim(r.Header.Get("If-Match"), `"`)
	if ifMatch == "" && !req.Create {
		writeError(w, 428, "precondition_required", "If-Match with the sha you loaded is required", nil)
		return
	}
	rel := path.Clean(r.URL.Query().Get("path"))
	sha, err := files.Write(a.Root, rel, []byte(req.Content), ifMatch, req.Create)
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{rel}) // no need to wait for the watcher to notice our own write
	writeJSON(w, 200, map[string]any{"sha": sha})
}

func fileError(w http.ResponseWriter, err error) {
	var ce *files.ConflictError
	switch {
	case errors.As(err, &ce):
		writeError(w, 409, "conflict", ce.Error(), map[string]string{"current_sha": ce.CurrentSHA})
	case errors.Is(err, files.ErrNotFound):
		writeError(w, 404, "not_found", err.Error(), nil)
	case errors.Is(err, files.ErrExists):
		writeError(w, 409, "exists", err.Error(), nil)
	case errors.Is(err, files.ErrBinary):
		writeError(w, 415, "binary", "this file is binary or not UTF-8; open it in another tool", nil)
	case errors.Is(err, files.ErrTooLarge):
		writeError(w, 413, "too_large", "file is too large to edit here (limit 2 MiB)", nil)
	case errors.Is(err, files.ErrForbidden):
		writeError(w, 403, "forbidden", err.Error(), nil)
	case errors.Is(err, files.ErrOutsideRepo):
		writeError(w, 400, "bad_path", err.Error(), nil)
	default:
		writeError(w, 500, "file_error", err.Error(), nil)
	}
}

func (a *API) gitStatus(w http.ResponseWriter, r *http.Request) {
	st, err := files.Status(r.Context(), a.Root)
	if err != nil {
		writeError(w, 500, "git_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, st)
}

func (a *API) gitDiff(w http.ResponseWriter, r *http.Request) {
	d, err := files.Diff(r.Context(), a.Root, r.URL.Query().Get("path"))
	if err != nil {
		if errors.Is(err, files.ErrOutsideRepo) {
			writeError(w, 400, "bad_path", err.Error(), nil)
			return
		}
		writeError(w, 500, "git_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"diff": d})
}
