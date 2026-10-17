package vars

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Editing ansible-vault data. Plaintext only ever travels through stdin and
// stdout of ansible-vault: it isn't written to disk, logged or cached.

// VaultLabel returns the vault ID in a ciphertext's header
// ($ANSIBLE_VAULT;1.2;AES256;<label>), or "" for an unlabelled 1.1 header.
func VaultLabel(ciphertext string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(ciphertext), "\n")
	parts := strings.Split(strings.TrimSpace(line), ";")
	if len(parts) >= 4 && parts[0] == "$ANSIBLE_VAULT" {
		return parts[3]
	}
	return ""
}

var (
	vaultIDRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	varNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	ErrNoVault = errors.New("not an ansible-vault value")
)

// ValidVaultID reports whether s may be used as a vault ID label.
func ValidVaultID(s string) bool { return vaultIDRe.MatchString(s) }

// VaultID is one vault identity a project can decrypt with.
type VaultID struct {
	Label  string `json:"label"`
	Source string `json:"source,omitempty"` // password file (absolute), "prompt" or a script
}

// VaultIDs lists the identities ansible-vault sees for files under abs: the
// password file (label "default") plus vault_identity_list from the
// environment or ansible.cfg.
func (s *Secrets) VaultIDs(abs string) []VaultID {
	vp := s.project(abs)
	var out []VaultID
	seen := map[string]bool{}
	add := func(label, src string) {
		if !seen[label] {
			seen[label] = true
			out = append(out, VaultID{Label: label, Source: src})
		}
	}
	if pw := s.defaultPasswordFile(vp); pw != "" {
		add("default", pw)
	}
	list := s.env("ANSIBLE_VAULT_IDENTITY_LIST")
	if list == "" {
		list = cfgValue(vp.Config, "vault_identity_list")
	}
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		label, src, ok := strings.Cut(e, "@")
		if !ok {
			label, src = "default", e
		}
		if src != "prompt" && !filepath.IsAbs(src) && !strings.HasPrefix(src, "~") {
			src = filepath.Join(vp.Dir, src)
		}
		add(label, expandHome(src))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label == "default" && out[j].Label != "default" })
	return out
}

func (s *Secrets) env(name string) string {
	for _, kv := range s.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v
		}
	}
	return ""
}

// defaultPasswordFile is the password file ansible-vault uses without a
// vault ID: groundwork.yaml's, then ANSIBLE_VAULT_PASSWORD_FILE, then ansible.cfg.
func (s *Secrets) defaultPasswordFile(vp VaultProject) string {
	if vp.PasswordFile != "" {
		return vp.PasswordFile
	}
	if v := s.env("ANSIBLE_VAULT_PASSWORD_FILE"); v != "" {
		return expandHome(v)
	}
	if v := cfgValue(vp.Config, "vault_password_file"); v != "" {
		v = expandHome(v)
		if !filepath.IsAbs(v) {
			v = filepath.Join(filepath.Dir(vp.Config), v)
		}
		return v
	}
	return ""
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// cfgValue reads one key from an ansible.cfg (any section; ansible keeps the
// vault settings under [defaults]).
func cfgValue(path, key string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			k, v, ok = strings.Cut(l, ":")
		}
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// encryptLabel picks the vault ID to encrypt with: the one asked for, else
// "default" when several identities would otherwise make ansible-vault refuse.
func (s *Secrets) encryptLabel(abs, label string) (string, error) {
	if label != "" {
		if !ValidVaultID(label) {
			return "", fmt.Errorf("invalid vault ID %q", label)
		}
		return label, nil
	}
	ids := s.VaultIDs(abs)
	if len(ids) > 1 {
		return ids[0].Label, nil // "default" sorts first when there is one
	}
	return "", nil
}

func (s *Secrets) ready(abs string) error {
	if _, err := s.LookPath("ansible-vault"); err != nil {
		return errors.New("ansible-vault isn't installed")
	}
	if !s.hasVaultPassword(s.project(abs)) {
		return errors.New("no vault password configured: set vault_password_file for this Ansible project in groundwork.yaml")
	}
	return nil
}

// Encrypt returns plaintext encrypted as a whole vault file, with the
// project's vault settings and the given vault ID ("" = the default).
func (s *Secrets) Encrypt(ctx context.Context, abs string, plaintext []byte, label string) ([]byte, error) {
	if err := s.ready(abs); err != nil {
		return nil, err
	}
	label, err := s.encryptLabel(abs, label)
	if err != nil {
		return nil, err
	}
	vp := s.project(abs)
	env, args := s.vaultArgs(vp)
	if label != "" {
		args = append(args, "--encrypt-vault-id="+label)
	}
	argv := append(append([]string{"ansible-vault", "encrypt"}, args...), "--output=-", "-")
	out, err := s.run(ctx, vp.Dir, env, plaintext, argv...)
	return []byte(out), err
}

// EncryptString encrypts value for key with the project's vault settings and
// returns the YAML `key: !vault |` block.
func (s *Secrets) EncryptString(ctx context.Context, abs, key, value string) (string, error) {
	return s.encryptString(ctx, abs, key, value, "")
}

func (s *Secrets) encryptString(ctx context.Context, abs, key, value, label string) (string, error) {
	if err := s.ready(abs); err != nil {
		return "", err
	}
	label, err := s.encryptLabel(abs, label)
	if err != nil {
		return "", err
	}
	vp := s.project(abs)
	env, args := s.vaultArgs(vp)
	if label != "" {
		args = append(args, "--encrypt-vault-id="+label)
	}
	argv := append(append([]string{"ansible-vault", "encrypt_string"}, args...), "--stdin-name="+key)
	out, err := s.run(ctx, vp.Dir, env, []byte(value), argv...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// blockRange returns the 1-based, inclusive lines of the top-level value that
// starts on line: the key line and every indented or blank line after it,
// minus trailing blank lines.
func blockRange(lines []string, line int) (int, int) {
	end := line
	for i := line; i < len(lines); i++ { // lines[i] is line i+1
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if l[0] != ' ' && l[0] != '\t' {
			break
		}
		end = i + 1
	}
	return line, end
}

func replaceBlock(src []byte, line int, block string) []byte {
	lines := strings.Split(string(src), "\n")
	start, end := blockRange(lines, line)
	out := append(append(append([]string{}, lines[:start-1]...), block), lines[end:]...)
	return []byte(strings.Join(out, "\n"))
}

// Update re-encrypts one vault secret (a whole file or an inline value) with a
// new plaintext and returns the file's new content. The value keeps its vault
// ID. changed is false, and next nil, when the value is the same: re-encrypting
// it anyway would only churn the ciphertext in git.
func (s *Secrets) Update(ctx context.Context, id, value string) (sec Secret, next []byte, changed bool, err error) {
	sec, src, err := s.find(ctx, id)
	if err != nil {
		return sec, nil, false, err
	}
	abs := filepath.Join(s.Root, filepath.FromSlash(sec.File))
	switch sec.Kind {
	case "vault-file":
		cur, err := s.vaultView(ctx, abs)
		if err != nil {
			return sec, nil, false, err
		}
		if cur == value {
			return sec, nil, false, nil
		}
		next, err = s.Encrypt(ctx, abs, []byte(value), sec.VaultID)
		return sec, next, err == nil, err
	case "vault-inline":
		for _, iv := range inlineVaults(src) {
			if iv.key != sec.Name {
				continue
			}
			cur, err := s.vaultDecrypt(ctx, abs, iv.ciphertext)
			if err != nil {
				return sec, nil, false, err
			}
			if cur == value {
				return sec, nil, false, nil
			}
			block, err := s.encryptString(ctx, abs, iv.key, value, sec.VaultID)
			if err != nil {
				return sec, nil, false, err
			}
			return sec, replaceBlock(src, iv.line, block), true, nil
		}
	}
	return sec, nil, false, ErrNoVault
}

// find looks a vault secret up by ID and returns it with its file's content.
func (s *Secrets) find(ctx context.Context, id string) (Secret, []byte, error) {
	for _, c := range s.List(ctx, false) {
		if c.ID != id {
			continue
		}
		if c.Kind != "vault-file" && c.Kind != "vault-inline" {
			return c, nil, ErrNoVault
		}
		if !c.Revealable {
			return c, nil, fmt.Errorf("%w: %s", ErrNotRevealable, firstNonEmptyStr(c.Message, c.State))
		}
		b, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(c.File)))
		return c, b, err
	}
	return Secret{}, nil, fmt.Errorf("%w: not found", ErrNotRevealable)
}

// AddInline appends `key: !vault |` to a YAML vars file (src may be empty for
// a new file). The key must not be set at the top level already.
func (s *Secrets) AddInline(ctx context.Context, abs string, src []byte, key, value, label string) ([]byte, error) {
	if !varNameRe.MatchString(key) {
		return nil, fmt.Errorf("%q isn't a valid Ansible variable name", key)
	}
	if len(bytes.TrimSpace(src)) > 0 {
		var root yaml.Node
		if err := yaml.Unmarshal(src, &root); err != nil {
			return nil, fmt.Errorf("the file isn't valid YAML: %v", err)
		}
		if len(root.Content) > 0 {
			m := root.Content[0]
			if m.Kind != yaml.MappingNode {
				return nil, errors.New("the file isn't a YAML mapping of variables")
			}
			for i := 0; i+1 < len(m.Content); i += 2 {
				if m.Content[i].Value == key {
					return nil, fmt.Errorf("%s is already set in this file (line %d)", key, m.Content[i].Line)
				}
			}
		}
	}
	block, err := s.encryptString(ctx, abs, key, value, label)
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, src...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(append(out, block...), '\n'), nil
}

// ---- rekey ----

// RekeyItem is one value a rekey re-encrypts; Problem says why it can't be.
type RekeyItem struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Problem string `json:"problem,omitempty"`
}

// RekeyPlan lists what a rekey of one project and vault ID would change.
type RekeyPlan struct {
	Items []RekeyItem `json:"items"`
	// PasswordFile is where this vault ID's password is read from, when
	// groundwork can write the new one there ("" otherwise, with the reason).
	PasswordFile string `json:"password_file,omitempty"`
	NoUpdate     string `json:"no_update,omitempty"`
}

func sameLabel(a, b string) bool {
	norm := func(s string) string {
		if s == "" {
			return "default"
		}
		return s
	}
	return norm(a) == norm(b)
}

func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// PlanRekey lists the vault values under dir encrypted with label.
func (s *Secrets) PlanRekey(ctx context.Context, dir, label string) RekeyPlan {
	plan := RekeyPlan{Items: []RekeyItem{}}
	for _, c := range s.List(ctx, true) {
		if (c.Kind != "vault-file" && c.Kind != "vault-inline") || !sameLabel(c.VaultID, label) {
			continue
		}
		abs := filepath.Join(s.Root, filepath.FromSlash(c.File))
		if !within(dir, abs) {
			continue
		}
		it := RekeyItem{ID: c.ID, Kind: c.Kind, Name: c.Name, File: c.File, Line: c.Line}
		if c.State != "can-decrypt" {
			it.Problem = firstNonEmptyStr(c.Message, c.State)
		}
		plan.Items = append(plan.Items, it)
	}
	src := ""
	for _, id := range s.VaultIDs(dir + string(filepath.Separator) + "x") {
		if sameLabel(id.Label, label) {
			src = id.Source
		}
	}
	switch st, err := os.Stat(src); {
	case src == "":
		plan.NoUpdate = "no password file is configured for this vault ID"
	case src == "prompt":
		plan.NoUpdate = "this vault ID asks for its password"
	case err != nil:
		plan.NoUpdate = err.Error()
	case !st.Mode().IsRegular() || st.Mode().Perm()&0o111 != 0:
		plan.NoUpdate = src + " is a script, not a password file"
	case !looksLikePasswordFile(src):
		// the path comes from the repository (groundwork.yaml, ansible.cfg):
		// never overwrite something that isn't a one-line password file
		plan.NoUpdate = src + " doesn't look like a password file (one line)"
	case len(plan.Items) == 0:
		plan.NoUpdate = "nothing is encrypted with this vault ID here"
	default:
		plan.PasswordFile = src
	}
	return plan
}

func looksLikePasswordFile(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil || len(b) > 1024 {
		return false
	}
	return len(bytes.TrimSpace(b)) > 0 && !bytes.ContainsAny(bytes.TrimRight(b, "\r\n"), "\r\n")
}

// Rekeyed is one file's content after a rekey, and its content before.
type Rekeyed struct {
	File      string
	Old, Next []byte
}

// Rekey re-encrypts the plan's values with password and returns the new
// content of every affected file. Nothing is written: the caller writes the
// files (and the password file) together, so a failure leaves nothing half done.
func (s *Secrets) Rekey(ctx context.Context, plan RekeyPlan, label, password string) ([]Rekeyed, error) {
	if password == "" || strings.ContainsAny(password, "\r\n") {
		return nil, errors.New("the new password must be one non-empty line")
	}
	if label != "" && !ValidVaultID(label) {
		return nil, fmt.Errorf("invalid vault ID %q", label)
	}
	byFile := map[string][]RekeyItem{}
	var order []string
	for _, it := range plan.Items {
		if it.Problem != "" {
			return nil, fmt.Errorf("%s in %s can't be decrypted (%s): fix it before rekeying", it.Name, it.File, it.Problem)
		}
		if byFile[it.File] == nil {
			order = append(order, it.File)
		}
		byFile[it.File] = append(byFile[it.File], it)
	}
	tmp, err := os.MkdirTemp("", "groundwork-rekey-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	pw, cfg := filepath.Join(tmp, "password"), filepath.Join(tmp, "ansible.cfg")
	if err := os.WriteFile(pw, []byte(password+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		return nil, err
	}
	// encrypt with the new password only: no ansible.cfg identities, no env ones
	var environ []string
	for _, kv := range s.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "ANSIBLE_CONFIG" && k != "ANSIBLE_VAULT_PASSWORD_FILE" && k != "ANSIBLE_VAULT_IDENTITY_LIST" {
			environ = append(environ, kv)
		}
	}
	environ = append(environ, "ANSIBLE_CONFIG="+cfg)
	idArg := "--vault-password-file=" + pw
	if label != "" && label != "default" {
		idArg = "--vault-id=" + label + "@" + pw
	}

	var out []Rekeyed
	for _, file := range order {
		abs := filepath.Join(s.Root, filepath.FromSlash(file))
		old, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		next := old
		items := byFile[file]
		sort.Slice(items, func(i, j int) bool { return items[i].Line > items[j].Line }) // bottom up keeps line numbers valid
		for _, it := range items {
			if it.Kind == "vault-file" {
				plain, err := s.vaultView(ctx, abs)
				if err != nil {
					return nil, err
				}
				ct, err := s.runWith(ctx, s.project(abs).Dir, environ, []byte(plain), "ansible-vault", "encrypt", idArg, "--output=-", "-")
				if err != nil {
					return nil, err
				}
				next = []byte(ct)
				continue
			}
			for _, iv := range inlineVaults(next) {
				if iv.key != it.Name {
					continue
				}
				plain, err := s.vaultDecrypt(ctx, abs, iv.ciphertext)
				if err != nil {
					return nil, err
				}
				block, err := s.runWith(ctx, s.project(abs).Dir, environ, []byte(plain), "ansible-vault", "encrypt_string", idArg, "--stdin-name="+iv.key)
				if err != nil {
					return nil, err
				}
				next = replaceBlock(next, iv.line, strings.TrimRight(block, "\n"))
			}
		}
		out = append(out, Rekeyed{File: file, Old: old, Next: next})
	}
	return out, nil
}
