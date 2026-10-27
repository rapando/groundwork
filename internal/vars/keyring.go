package vars

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Vault passwords typed in the console. A project without a password file
// (or with a `label@prompt` vault identity) is unlocked by typing the
// password; it stays in this process's memory until it is locked or the
// service stops, and reaches ansible through a pipe (/dev/fd/N), never a
// file, an argument or an environment variable.

// Keyring holds typed vault passwords per Ansible project and vault ID. The
// zero value is not usable; a nil *Keyring holds nothing.
type Keyring struct {
	mu  sync.Mutex
	pw  map[string]string
	gen int // bumped on every change, so cached decrypt checks are redone
}

func NewKeyring() *Keyring { return &Keyring{pw: map[string]string{}} }

func normLabel(l string) string {
	if l == "" {
		return "default"
	}
	return l
}

func keyringKey(dir, label string) string { return filepath.Clean(dir) + "\x00" + normLabel(label) }

func (k *Keyring) Set(dir, label, pw string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.pw[keyringKey(dir, label)] = pw
	k.gen++
	k.mu.Unlock()
}

func (k *Keyring) Get(dir, label string) (string, bool) {
	if k == nil {
		return "", false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	pw, ok := k.pw[keyringKey(dir, label)]
	return pw, ok
}

func (k *Keyring) Forget(dir, label string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	if _, ok := k.pw[keyringKey(dir, label)]; ok {
		delete(k.pw, keyringKey(dir, label))
		k.gen++
	}
	k.mu.Unlock()
}

// Values returns every typed password, for redacting run logs.
func (k *Keyring) Values() []string {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.pw))
	for _, v := range k.pw {
		out = append(out, v)
	}
	return out
}

// Generation changes whenever a password is typed or forgotten.
func (k *Keyring) Generation() int {
	if k == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gen
}

// identity is one vault identity of a project, as configured.
type identity struct {
	label  string
	src    string // absolute password file or script, or "prompt"
	listed bool   // from vault_identity_list (else the default password file, or typed)
}

func (id identity) prompt() bool { return id.src == "prompt" }

// identities lists the vault identities for vp: the default password file,
// vault_identity_list from the environment or ansible.cfg, and, when neither
// gives one, a "default" identity whose password is typed in the console.
func (s *Secrets) identities(vp VaultProject) []identity {
	var out []identity
	seen := map[string]bool{}
	add := func(id identity) {
		if !seen[id.label] {
			seen[id.label] = true
			out = append(out, id)
		}
	}
	if pw := s.defaultPasswordFile(vp); pw != "" {
		add(identity{label: "default", src: pw})
	}
	for _, e := range strings.Split(s.identityList(vp), ",") {
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
		add(identity{label: label, src: expandHome(src), listed: true})
	}
	if len(out) == 0 {
		add(identity{label: "default", src: "prompt"})
	}
	return out
}

func (s *Secrets) identityList(vp VaultProject) string {
	if v := s.env("ANSIBLE_VAULT_IDENTITY_LIST"); v != "" {
		return v
	}
	return cfgValue(vp.Config, "vault_identity_list")
}

// usable reports whether ansible can get id's password: from its file or
// script, or because it was typed.
func (s *Secrets) usable(vp VaultProject, id identity) bool {
	if !id.prompt() {
		return true
	}
	_, ok := s.Keyring.Get(vp.Dir, id.label)
	return ok
}

// locked reports whether a value encrypted with label can't be opened only
// because its password hasn't been typed yet.
func (s *Secrets) locked(vp VaultProject, label string) bool {
	ids := s.identities(vp)
	for _, id := range ids {
		if label != "" && id.label == label {
			return !s.usable(vp, id)
		}
	}
	// unlabelled (or an unknown label): ansible tries every identity
	for _, id := range ids {
		if s.usable(vp, id) {
			return false
		}
	}
	return true
}

// VaultFlags is how an ansible command gets the typed vault passwords:
// environment and arguments to add, and Passwords to pass as pipes at
// /dev/fd/firstFD, firstFD+1, … (see proc.SecretPipes).
type VaultFlags struct {
	Env       []string
	Args      []string
	Passwords []string
}

// TypedVault returns the flags for typed passwords of the Ansible project
// at dir. Password files are left to the caller and ansible.cfg, except that
// a vault_identity_list with `prompt` entries is passed in the environment
// with those entries pointing at the pipes (or dropped while locked), so
// ansible never stops to ask for a password nobody can type.
func (s *Secrets) TypedVault(dir string, firstFD int) VaultFlags {
	return s.typedFlags(s.project(filepath.Join(dir, "x")), firstFD)
}

func (s *Secrets) typedFlags(vp VaultProject, firstFD int) VaultFlags {
	var f VaultFlags
	pipe := func(pw string) string {
		f.Passwords = append(f.Passwords, pw)
		return fmt.Sprintf("/dev/fd/%d", firstFD+len(f.Passwords)-1)
	}
	ids := s.identities(vp)
	var list []string
	rewrite := false
	for _, id := range ids {
		switch {
		case !id.prompt():
			if id.listed {
				list = append(list, id.label+"@"+id.src)
			}
		case id.listed:
			rewrite = true
			if pw, ok := s.Keyring.Get(vp.Dir, id.label); ok {
				list = append(list, id.label+"@"+pipe(pw))
			}
		default: // the typed default of a project with no password configured
			if pw, ok := s.Keyring.Get(vp.Dir, id.label); ok {
				f.Args = append(f.Args, "--vault-password-file="+pipe(pw))
			}
		}
	}
	if rewrite {
		f.Env = append(f.Env, "ANSIBLE_VAULT_IDENTITY_LIST="+strings.Join(list, ","))
	}
	return f
}

// ErrWrongPassword: a typed vault password doesn't open the project's values.
var ErrWrongPassword = errors.New("that password doesn't open this vault")

func checkPassword(pw string) error {
	if pw == "" || strings.ContainsAny(pw, "\r\n") || len(pw) > 1024 {
		return errors.New("the password must be one non-empty line")
	}
	return nil
}

// Unlock keeps pw as the typed password of one vault ID of the Ansible
// project at dir, after checking that it opens a value encrypted with that
// ID. checked is false when nothing is encrypted with it yet: pw then becomes
// the password new values are encrypted with.
func (s *Secrets) Unlock(ctx context.Context, dir, label, pw string) (checked bool, err error) {
	if err := checkPassword(pw); err != nil {
		return false, err
	}
	label = normLabel(label)
	vp := s.project(filepath.Join(dir, "x"))
	var id *identity
	for _, x := range s.identities(vp) {
		if x.label == label {
			id = &x
			break
		}
	}
	if id == nil {
		return false, fmt.Errorf("no vault ID %s in this project", label)
	}
	if !id.prompt() {
		return false, fmt.Errorf("vault ID %s reads its password from %s", label, id.src)
	}
	for _, c := range s.List(ctx, false) {
		if (c.Kind != "vault-file" && c.Kind != "vault-inline") || !sameLabel(c.VaultID, label) {
			continue
		}
		abs := filepath.Join(s.Root, filepath.FromSlash(c.File))
		if !within(vp.Dir, abs) || s.project(abs).Dir != vp.Dir {
			continue
		}
		ct, err := s.ciphertext(c)
		if err != nil {
			return false, err
		}
		if _, err := s.decryptOnly(ctx, vp.Dir, ct, label, pw); err != nil {
			if strings.Contains(err.Error(), "Decryption failed") || strings.Contains(err.Error(), "no vault secrets") {
				return false, ErrWrongPassword
			}
			return false, err
		}
		checked = true
		break
	}
	s.Keyring.Set(vp.Dir, label, pw)
	return checked, nil
}

// Lock forgets a typed password.
func (s *Secrets) Lock(dir, label string) {
	s.Keyring.Forget(s.project(filepath.Join(dir, "x")).Dir, label)
}

// ciphertext returns a vault secret's encrypted text.
func (s *Secrets) ciphertext(c Secret) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(c.File)))
	if err != nil || c.Kind == "vault-file" {
		return b, err
	}
	for _, iv := range inlineVaults(b) {
		if iv.key == c.Name {
			return []byte(iv.ciphertext), nil
		}
	}
	return nil, ErrNoVault
}

// decryptOnly decrypts ciphertext with pw alone: no ansible.cfg, password
// file or identity list takes part, so success means pw is right.
func (s *Secrets) decryptOnly(ctx context.Context, dir string, ciphertext []byte, label, pw string) (string, error) {
	environ, cleanup, err := s.isolatedEnv()
	if err != nil {
		return "", err
	}
	defer cleanup()
	return s.runWith(ctx, dir, environ, ciphertext, []string{pw}, "ansible-vault", "decrypt", vaultIDArg(label, "/dev/fd/3"), "--output=-")
}

// vaultIDArg names one password source for one vault ID.
func vaultIDArg(label, src string) string {
	if label == "" || label == "default" {
		return "--vault-password-file=" + src
	}
	return "--vault-id=" + label + "@" + src
}

// isolatedEnv is the service's environment without any vault setting, and
// with an empty ansible.cfg.
func (s *Secrets) isolatedEnv() ([]string, func(), error) {
	tmp, err := os.MkdirTemp("", "groundwork-vault-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	cfg := filepath.Join(tmp, "ansible.cfg")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	var environ []string
	for _, kv := range s.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "ANSIBLE_CONFIG" && k != "ANSIBLE_VAULT_PASSWORD_FILE" && k != "ANSIBLE_VAULT_IDENTITY_LIST" {
			environ = append(environ, kv)
		}
	}
	return append(environ, "ANSIBLE_CONFIG="+cfg), cleanup, nil
}
