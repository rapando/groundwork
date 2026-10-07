package vars

import (
	"bytes"
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
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Secret is one encrypted value (or store) groundwork knows about. Values are
// never part of it; Reveal decrypts one on request.
type Secret struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // sops | vault-file | vault-inline | env
	Name       string `json:"name"`
	Store      string `json:"store"` // human label: "SOPS", "ansible-vault", "environment"
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	State      string `json:"state"` // can-decrypt | wrong-password | no-password | tool-missing | cannot-decrypt | names-only
	Message    string `json:"message,omitempty"`
	Revealable bool   `json:"revealable"`
}

// VaultProject tells the decryptor how to open vault data under Dir.
type VaultProject struct {
	Dir          string // absolute project dir
	PasswordFile string // absolute, or "" (then ansible.cfg / env decide)
	Config       string // absolute ansible.cfg, or ""
}

// Secrets discovers and decrypts secrets. It caches decryptability per file
// content so listing stays cheap; it never caches plaintext.
type Secrets struct {
	Root     string
	Projects []VaultProject
	Environ  func() []string
	LookPath func(string) (string, error)

	mu    sync.Mutex
	cache map[string]checkResult // file sha → result
}

type checkResult struct{ state, msg string }

func NewSecrets(root string, projects []VaultProject) *Secrets {
	return &Secrets{Root: root, Projects: projects, Environ: os.Environ, LookPath: exec.LookPath, cache: map[string]checkResult{}}
}

// credential env vars: names are shown, values never.
var credentialEnv = []string{
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE",
	"ARM_CLIENT_ID", "ARM_CLIENT_SECRET", "ARM_SUBSCRIPTION_ID", "ARM_TENANT_ID",
	"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CREDENTIALS", "CLOUDSDK_AUTH_ACCESS_TOKEN",
	"DIGITALOCEAN_TOKEN", "HCLOUD_TOKEN", "CLOUDFLARE_API_TOKEN", "LINODE_TOKEN",
	"VAULT_TOKEN", "VAULT_ADDR", "TF_TOKEN_app_terraform_io",
	"ANSIBLE_VAULT_PASSWORD_FILE", "SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY", "SOPS_KMS_ARN", "SOPS_PGP_FP",
}

const vaultHeader = "$ANSIBLE_VAULT;"

type sopsFile struct {
	keys  []string
	lines map[string]int
}

// sopsKeys reads the top-level keys of a SOPS-encrypted YAML/JSON file (SOPS
// leaves keys in clear text). ok is false if the file isn't SOPS.
func sopsKeys(path string, b []byte) (sopsFile, bool) {
	sf := sopsFile{lines: map[string]int{}}
	if strings.HasSuffix(path, ".json") {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil || m["sops"] == nil {
			return sf, false
		}
		for k := range m {
			if k != "sops" {
				sf.keys = append(sf.keys, k)
			}
		}
		sort.Strings(sf.keys)
		return sf, true
	}
	var root yaml.Node
	if yaml.Unmarshal(b, &root) != nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return sf, false
	}
	m := root.Content[0]
	isSops := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Value == "sops" && v.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(v.Content); j += 2 {
				if v.Content[j].Value == "mac" {
					isSops = true
				}
			}
			continue
		}
		sf.keys = append(sf.keys, k.Value)
		sf.lines[k.Value] = k.Line
	}
	return sf, isSops
}

type inlineVault struct {
	key        string
	line       int
	ciphertext string
}

// inlineVaults finds top-level `key: !vault |` values.
func inlineVaults(b []byte) []inlineVault {
	var root yaml.Node
	if yaml.Unmarshal(b, &root) != nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	var out []inlineVault
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if v := m.Content[i+1]; v.Tag == "!vault" {
			out = append(out, inlineVault{key: m.Content[i].Value, line: m.Content[i].Line, ciphertext: v.Value})
		}
	}
	return out
}

func skipDir(name string) bool {
	switch name {
	case ".git", ".groundwork", ".terraform", "node_modules", ".venv", "venv", "dist":
		return true
	}
	return false
}

func secretID(parts ...string) string { return strings.Join(parts, "\x1f") }

// List discovers secrets. checks controls whether decryptability is tested
// (it shells out to ansible-vault / sops, once per distinct file content).
func (s *Secrets) List(ctx context.Context, checks bool) []Secret {
	var out []Secret
	_ = filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			if p != s.Root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(p)
		if ext != ".yml" && ext != ".yaml" && ext != ".json" && ext != "" && ext != ".env" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(s.Root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case bytes.HasPrefix(bytes.TrimSpace(b), []byte(vaultHeader)):
			sec := Secret{ID: secretID("vault-file", rel), Kind: "vault-file", Name: filepath.Base(rel), Store: "ansible-vault (whole file)", File: rel, Line: 1}
			s.vaultState(ctx, &sec, p, b, checks, func() (string, error) { return s.vaultView(ctx, p) })
			out = append(out, sec)
		case ext == ".yml" || ext == ".yaml" || ext == ".json":
			if sf, ok := sopsKeys(p, b); ok {
				for _, k := range sf.keys {
					sec := Secret{ID: secretID("sops", rel, k), Kind: "sops", Name: k, Store: "SOPS", File: rel, Line: sf.lines[k]}
					s.sopsState(ctx, &sec, p, b, checks)
					out = append(out, sec)
				}
				return nil
			}
			if ext == ".json" || !bytes.Contains(b, []byte("!vault")) {
				return nil
			}
			for _, iv := range inlineVaults(b) {
				sec := Secret{ID: secretID("vault-inline", rel, iv.key), Kind: "vault-inline", Name: iv.key, Store: "ansible-vault", File: rel, Line: iv.line}
				ct := iv.ciphertext
				s.vaultState(ctx, &sec, p, []byte(ct), checks, func() (string, error) { return s.vaultDecrypt(ctx, p, ct) })
				out = append(out, sec)
			}
		}
		return nil
	})
	environ := map[string]bool{}
	for _, kv := range s.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok {
			environ[k] = true
		}
	}
	for _, name := range credentialEnv {
		if environ[name] {
			out = append(out, Secret{ID: secretID("env", name), Kind: "env", Name: name, Store: "environment", State: "names-only",
				Message: "Set in groundwork's environment; passed to terraform and ansible. Values are never shown."})
		}
	}
	return out
}

func (s *Secrets) cached(key string, check func() checkResult) checkResult {
	s.mu.Lock()
	r, ok := s.cache[key]
	s.mu.Unlock()
	if ok {
		return r
	}
	r = check()
	s.mu.Lock()
	s.cache[key] = r
	s.mu.Unlock()
	return r
}

func contentKey(kind string, b []byte, extra string) string {
	h := sha256.Sum256(append([]byte(kind+"\x00"+extra+"\x00"), b...))
	return hex.EncodeToString(h[:])
}

// SetProjects replaces the vault settings (on config reload).
func (s *Secrets) SetProjects(ps []VaultProject) {
	s.mu.Lock()
	s.Projects = ps
	s.mu.Unlock()
}

// project returns the vault settings for a file (the innermost project).
func (s *Secrets) project(abs string) VaultProject {
	s.mu.Lock()
	projects := s.Projects
	s.mu.Unlock()
	best := VaultProject{}
	for _, p := range projects {
		if (abs == p.Dir || strings.HasPrefix(abs, p.Dir+string(filepath.Separator))) && len(p.Dir) > len(best.Dir) {
			best = p
		}
	}
	if best.Dir == "" {
		best.Dir = filepath.Dir(abs)
	}
	return best
}

func (s *Secrets) hasVaultPassword(vp VaultProject) bool {
	if vp.PasswordFile != "" {
		return true
	}
	for _, kv := range s.Environ() {
		if strings.HasPrefix(kv, "ANSIBLE_VAULT_PASSWORD_FILE=") || strings.HasPrefix(kv, "ANSIBLE_VAULT_IDENTITY_LIST=") {
			return true
		}
	}
	if vp.Config != "" {
		if b, err := os.ReadFile(vp.Config); err == nil {
			t := string(b)
			return strings.Contains(t, "vault_password_file") || strings.Contains(t, "vault_identity_list")
		}
	}
	return false
}

func (s *Secrets) vaultState(ctx context.Context, sec *Secret, abs string, content []byte, check bool, decrypt func() (string, error)) {
	if _, err := s.LookPath("ansible-vault"); err != nil {
		sec.State, sec.Message = "tool-missing", "ansible-vault isn't installed"
		return
	}
	vp := s.project(abs)
	if !s.hasVaultPassword(vp) {
		sec.State, sec.Message = "no-password", "Set vault_password_file for this Ansible project in groundwork.yaml"
		return
	}
	sec.Revealable = true
	sec.State = "unchecked"
	if !check {
		return
	}
	r := s.cached(contentKey("vault", content, vp.PasswordFile), func() checkResult {
		if _, err := decrypt(); err != nil {
			if strings.Contains(err.Error(), "Decryption failed") || strings.Contains(err.Error(), "no vault secrets") {
				return checkResult{"wrong-password", "The vault password doesn't open this value"}
			}
			return checkResult{"cannot-decrypt", err.Error()}
		}
		return checkResult{"can-decrypt", ""}
	})
	sec.State, sec.Message = r.state, r.msg
	sec.Revealable = r.state == "can-decrypt"
}

func (s *Secrets) sopsState(ctx context.Context, sec *Secret, abs string, content []byte, check bool) {
	if _, err := s.LookPath("sops"); err != nil {
		sec.State, sec.Message = "tool-missing", "Install the sops CLI to decrypt this file"
		return
	}
	sec.Revealable, sec.State = true, "unchecked"
	if !check {
		return
	}
	r := s.cached(contentKey("sops", content, ""), func() checkResult {
		if _, err := s.run(ctx, filepath.Dir(abs), nil, nil, "sops", "--decrypt", "--", abs); err != nil {
			return checkResult{"cannot-decrypt", err.Error()}
		}
		return checkResult{"can-decrypt", ""}
	})
	sec.State, sec.Message = r.state, r.msg
	sec.Revealable = r.state == "can-decrypt"
}

// run executes a decryption tool. Its stdout is the secret: it's returned and
// never logged. Errors carry the first line of stderr only.
func (s *Secrets) run(ctx context.Context, dir string, env []string, stdin []byte, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	bin, err := s.LookPath(argv[0])
	if err != nil {
		return "", fmt.Errorf("%s isn't installed", argv[0])
	}
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(s.Environ(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(strings.TrimPrefix(msg, "ERROR! "))
	}
	return stdout.String(), nil
}

func (s *Secrets) vaultArgs(vp VaultProject) (env []string, args []string) {
	if vp.Config != "" {
		env = append(env, "ANSIBLE_CONFIG="+vp.Config)
	}
	if vp.PasswordFile != "" {
		args = append(args, "--vault-password-file="+vp.PasswordFile)
	}
	return env, args
}

func (s *Secrets) vaultView(ctx context.Context, abs string) (string, error) {
	vp := s.project(abs)
	env, args := s.vaultArgs(vp)
	argv := append(append([]string{"ansible-vault", "view"}, args...), "--", abs)
	return s.run(ctx, vp.Dir, append(env, "PAGER=cat"), nil, argv...)
}

func (s *Secrets) vaultDecrypt(ctx context.Context, abs, ciphertext string) (string, error) {
	vp := s.project(abs)
	env, args := s.vaultArgs(vp)
	argv := append(append([]string{"ansible-vault", "decrypt"}, args...), "--output=-")
	return s.run(ctx, vp.Dir, env, []byte(ciphertext), argv...)
}

// EncryptString encrypts value for key with the project's vault settings and
// returns the YAML `key: !vault |` block.
func (s *Secrets) EncryptString(ctx context.Context, abs, key, value string) (string, error) {
	if _, err := s.LookPath("ansible-vault"); err != nil {
		return "", errors.New("ansible-vault isn't installed")
	}
	vp := s.project(abs)
	if !s.hasVaultPassword(vp) {
		return "", errors.New("no vault password configured: set vault_password_file for this Ansible project in groundwork.yaml")
	}
	env, args := s.vaultArgs(vp)
	argv := append(append([]string{"ansible-vault", "encrypt_string"}, args...), "--stdin-name="+key)
	out, err := s.run(ctx, vp.Dir, env, []byte(value), argv...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// ErrNotRevealable: the secret can't be revealed (unknown, env, or locked).
var ErrNotRevealable = errors.New("this secret can't be revealed")

// Reveal decrypts one secret by ID. The caller must not log or store the result.
func (s *Secrets) Reveal(ctx context.Context, id string) (Secret, string, error) {
	var sec *Secret
	for _, c := range s.List(ctx, false) {
		if c.ID == id {
			sec = &c
			break
		}
	}
	if sec == nil {
		return Secret{}, "", fmt.Errorf("%w: not found", ErrNotRevealable)
	}
	if !sec.Revealable {
		return *sec, "", fmt.Errorf("%w: %s", ErrNotRevealable, firstNonEmptyStr(sec.Message, sec.State))
	}
	abs := filepath.Join(s.Root, filepath.FromSlash(sec.File))
	switch sec.Kind {
	case "vault-file":
		v, err := s.vaultView(ctx, abs)
		return *sec, v, err
	case "vault-inline":
		b, err := os.ReadFile(abs)
		if err != nil {
			return *sec, "", err
		}
		for _, iv := range inlineVaults(b) {
			if iv.key == sec.Name {
				v, err := s.vaultDecrypt(ctx, abs, iv.ciphertext)
				return *sec, v, err
			}
		}
	case "sops":
		path, _ := json.Marshal([]string{sec.Name})
		v, err := s.run(ctx, filepath.Dir(abs), nil, nil, "sops", "--decrypt", "--extract", string(path), "--", abs)
		return *sec, strings.TrimRight(v, "\n"), err
	}
	return *sec, "", ErrNotRevealable
}

func firstNonEmptyStr(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// PlainAssignment returns the key and plaintext value of a top-level
// `key: value` line in a YAML vars file (1-based line).
func PlainAssignment(src []byte, line int) (key, value string, err error) {
	lines := strings.Split(string(src), "\n")
	if line < 1 || line > len(lines) {
		return "", "", fmt.Errorf("line %d is out of range", line)
	}
	l := lines[line-1]
	if l != strings.TrimLeft(l, " \t") {
		return "", "", errors.New("only top-level variables can be moved to the vault automatically")
	}
	k, v, ok := strings.Cut(l, ":")
	k = strings.TrimSpace(k)
	if !ok || k == "" || strings.ContainsAny(k, " #") {
		return "", "", fmt.Errorf("line %d isn't a `key: value` line", line)
	}
	if t := strings.TrimSpace(v); strings.HasPrefix(t, "!") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "&") || strings.HasPrefix(t, "*") {
		return "", "", errors.New("only single-line plain values can be moved to the vault automatically")
	}
	var m map[string]any
	if yaml.Unmarshal([]byte(l), &m) != nil {
		return "", "", fmt.Errorf("line %d isn't a single-line YAML value", line)
	}
	val, isStr := m[k].(string)
	if !isStr {
		if m[k] == nil {
			return "", "", errors.New("only single-line plain values can be moved to the vault automatically")
		}
		val = fmt.Sprint(m[k])
	}
	if strings.Contains(val, "{{") {
		return "", "", errors.New("this value is a template, not a secret")
	}
	return k, val, nil
}

// ReplaceLine swaps one line for a block of lines.
func ReplaceLine(src []byte, line int, block string) []byte {
	lines := strings.Split(string(src), "\n")
	lines[line-1] = block
	return []byte(strings.Join(lines, "\n"))
}
