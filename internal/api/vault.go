package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/vars"
)

// ansible-vault editing. Plaintext arrives in the request and goes to
// ansible-vault's stdin; it isn't logged, stored or written to disk. Every
// response is no-store.

// vaultProject is what the console needs to offer vault actions for one
// Ansible project.
type vaultProject struct {
	Path string         `json:"path"`
	IDs  []vars.VaultID `json:"ids"`
}

func (a *API) vaultProjects(cfg *config.Config) []vaultProject {
	out := []vaultProject{}
	if cfg == nil {
		return out
	}
	s := a.secretStore()
	for _, p := range cfg.Ansible.Projects {
		dir := filepath.Join(a.Root, filepath.FromSlash(p.Path))
		ids := s.VaultIDs(filepath.Join(dir, "x"))
		if ids == nil {
			ids = []vars.VaultID{}
		}
		out = append(out, vaultProject{Path: path.Clean(p.Path), IDs: ids})
	}
	return out
}

func vaultError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vars.ErrNotRevealable), errors.Is(err, vars.ErrNoVault):
		writeError(w, 422, "not_editable", err.Error(), nil)
	default:
		writeError(w, 422, "vault_failed", err.Error(), nil)
	}
}

func validVaultID(w http.ResponseWriter, id string) bool {
	if id != "" && !vars.ValidVaultID(id) {
		writeError(w, 400, "bad_vault_id", "invalid vault ID "+id, nil)
		return false
	}
	return true
}

type vaultUpdateReq struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	SHA   string `json:"sha"` // the file's sha when the value was revealed
}

// updateVault re-encrypts one vault file or inline value with new plaintext.
func (a *API) updateVault(w http.ResponseWriter, r *http.Request) {
	var req vaultUpdateReq
	if !decode(w, r, &req) {
		return
	}
	noStore(w)
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	sec, next, changed, err := a.secretStore().Update(ctx, req.ID, req.Value)
	if err != nil {
		vaultError(w, err)
		return
	}
	if !changed {
		writeJSON(w, 200, map[string]any{"file": sec.File, "sha": req.SHA, "changed": false})
		return
	}
	sha, err := files.Write(a.Root, sec.File, next, req.SHA, false)
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{sec.File})
	writeJSON(w, 200, map[string]any{"file": sec.File, "sha": sha, "changed": true})
}

type vaultCreateReq struct {
	File    string `json:"file"`
	Value   string `json:"value"`
	VaultID string `json:"vault_id"`
}

// createVault writes a new file encrypted with ansible-vault.
func (a *API) createVault(w http.ResponseWriter, r *http.Request) {
	var req vaultCreateReq
	if !decode(w, r, &req) || !validVaultID(w, req.VaultID) {
		return
	}
	noStore(w)
	switch _, err := files.Read(a.Root, req.File); {
	case err == nil:
		writeError(w, 409, "exists", req.File+" already exists", nil)
		return
	case !errors.Is(err, files.ErrNotFound):
		fileError(w, err)
		return
	}
	ct, err := a.secretStore().Encrypt(r.Context(), filepath.Join(a.Root, filepath.FromSlash(req.File)), []byte(req.Value), req.VaultID)
	if err != nil {
		vaultError(w, err)
		return
	}
	sha, err := files.Write(a.Root, req.File, ct, "", true)
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{req.File})
	writeJSON(w, 200, map[string]any{"file": req.File, "sha": sha})
}

type vaultAddReq struct {
	File    string `json:"file"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	VaultID string `json:"vault_id"`
}

// addVault adds one `key: !vault |` value to a YAML vars file, creating the
// file if it doesn't exist.
func (a *API) addVault(w http.ResponseWriter, r *http.Request) {
	var req vaultAddReq
	if !decode(w, r, &req) || !validVaultID(w, req.VaultID) {
		return
	}
	noStore(w)
	if ext := path.Ext(req.File); ext != ".yml" && ext != ".yaml" {
		writeError(w, 422, "unsupported", "encrypted variables go in a YAML vars file (.yml or .yaml)", nil)
		return
	}
	var src []byte
	sha := ""
	switch c, err := files.Read(a.Root, req.File); {
	case err == nil:
		if strings.HasPrefix(strings.TrimSpace(c.Text), "$ANSIBLE_VAULT;") {
			writeError(w, 422, "unsupported", req.File+" is encrypted as a whole file: edit it instead", nil)
			return
		}
		src, sha = []byte(c.Text), c.SHA
	case !errors.Is(err, files.ErrNotFound):
		fileError(w, err)
		return
	}
	next, err := a.secretStore().AddInline(r.Context(), filepath.Join(a.Root, filepath.FromSlash(req.File)), src, req.Key, req.Value, req.VaultID)
	if err != nil {
		vaultError(w, err)
		return
	}
	newSHA, err := files.Write(a.Root, req.File, next, sha, sha == "")
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{req.File})
	writeJSON(w, 200, map[string]any{"file": req.File, "sha": newSHA})
}

type vaultEncryptReq struct {
	File    string `json:"file"`
	VaultID string `json:"vault_id"`
}

// encryptVaultFile encrypts an existing plaintext file in place.
func (a *API) encryptVaultFile(w http.ResponseWriter, r *http.Request) {
	var req vaultEncryptReq
	if !decode(w, r, &req) || !validVaultID(w, req.VaultID) {
		return
	}
	noStore(w)
	c, err := files.Read(a.Root, req.File)
	if err != nil {
		fileError(w, err)
		return
	}
	if strings.HasPrefix(strings.TrimSpace(c.Text), "$ANSIBLE_VAULT;") {
		writeError(w, 422, "already_encrypted", req.File+" is already encrypted", nil)
		return
	}
	ct, err := a.secretStore().Encrypt(r.Context(), filepath.Join(a.Root, filepath.FromSlash(req.File)), []byte(c.Text), req.VaultID)
	if err != nil {
		vaultError(w, err)
		return
	}
	sha, err := files.Write(a.Root, req.File, ct, c.SHA, false)
	if err != nil {
		fileError(w, err)
		return
	}
	a.Checks.RunNow([]string{req.File})
	writeJSON(w, 200, map[string]any{"file": req.File, "sha": sha,
		"note": "The plaintext is still in git history if this file was committed: rotate what it holds."})
}

type vaultRekeyReq struct {
	Project            string `json:"project"`
	VaultID            string `json:"vault_id"`
	Password           string `json:"password"`
	UpdatePasswordFile bool   `json:"update_password_file"`
	Apply              bool   `json:"apply"`
}

// rekeyVault re-encrypts every vault value of one project and vault ID with
// a new password: a preview lists them, apply writes every file (and, if
// asked, the new password to the password file) or, on any failure, none.
func (a *API) rekeyVault(w http.ResponseWriter, r *http.Request) {
	var req vaultRekeyReq
	if !decode(w, r, &req) || !validVaultID(w, req.VaultID) {
		return
	}
	noStore(w)
	cfg, _ := a.config()
	if cfg == nil || !slices.ContainsFunc(cfg.Ansible.Projects, func(p config.AnsibleProject) bool { return path.Clean(p.Path) == path.Clean(req.Project) }) {
		writeError(w, 404, "not_found", "no Ansible project "+req.Project, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	s := a.secretStore()
	dir := filepath.Join(a.Root, filepath.FromSlash(path.Clean(req.Project)))
	plan := s.PlanRekey(ctx, dir, req.VaultID)
	if !req.Apply {
		writeJSON(w, 200, plan)
		return
	}
	if len(plan.Items) == 0 {
		writeError(w, 422, "nothing_to_rekey", "nothing in "+req.Project+" is encrypted with this vault ID", nil)
		return
	}
	if req.UpdatePasswordFile && plan.PasswordFile == "" {
		writeError(w, 422, "no_password_file", "can't update the password file: "+plan.NoUpdate, nil)
		return
	}
	next, err := s.Rekey(ctx, plan, req.VaultID, req.Password)
	if err != nil {
		vaultError(w, err)
		return
	}

	var oldPW []byte
	if req.UpdatePasswordFile {
		if oldPW, err = os.ReadFile(plan.PasswordFile); err == nil {
			err = writePrivate(plan.PasswordFile, []byte(req.Password+"\n"))
		}
		if err != nil {
			writeError(w, 500, "password_file", "couldn't write the new password: "+err.Error(), nil)
			return
		}
	}
	var written []vars.Rekeyed
	for _, f := range next {
		if _, err := files.Write(a.Root, f.File, f.Next, files.SHA(f.Old), false); err != nil {
			// put back what was written, so the old password still opens everything
			for _, done := range written {
				_, _ = files.Write(a.Root, done.File, done.Old, files.SHA(done.Next), false)
			}
			if oldPW != nil {
				_ = writePrivate(plan.PasswordFile, oldPW)
			}
			fileError(w, err)
			return
		}
		written = append(written, f)
	}
	changed := make([]string, len(written))
	for i, f := range written {
		changed[i] = f.File
	}
	a.Checks.RunNow(changed)
	writeJSON(w, 200, map[string]any{"files": changed, "values": len(plan.Items), "password_file_updated": req.UpdatePasswordFile})
}

// writePrivate replaces a file atomically with mode 0600.
func writePrivate(p string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".groundwork-pw-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
