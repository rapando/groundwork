// Package config defines groundwork.yaml: schema, load/save and validation.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const FileName = "groundwork.yaml"

type Config struct {
	Version   int       `yaml:"version"`
	Mode      string    `yaml:"mode"` // standalone | embedded
	Terraform Terraform `yaml:"terraform,omitempty"`
	Ansible   Ansible   `yaml:"ansible,omitempty"`
	Checks    Checks    `yaml:"checks,omitempty"`
	Drift     Drift     `yaml:"drift,omitempty"`
	Notify    *Notify   `yaml:"notify,omitempty"`
	Retention Retention `yaml:"retention,omitempty"`
	Ignore    []string  `yaml:"ignore,omitempty"`

	// Warnings are non-fatal findings from parsing: unknown top-level keys,
	// which a newer release may have written. They are shown, not saved.
	Warnings []string `yaml:"-" json:"-"`
}

// Retention bounds the run history kept per project. Zero means the default
// (500 runs, 30 days); runs still in progress or awaiting approval are kept.
type Retention struct {
	Runs int `yaml:"runs,omitempty"`
	Days int `yaml:"days,omitempty"`
}

const (
	DefaultRetentionRuns = 500
	DefaultRetentionDays = 30
	DefaultRunTimeout    = 2 * time.Hour
)

func (r Retention) RunsOrDefault() int {
	if r.Runs > 0 {
		return r.Runs
	}
	return DefaultRetentionRuns
}

func (r Retention) MaxAge() time.Duration {
	if r.Days > 0 {
		return time.Duration(r.Days) * 24 * time.Hour
	}
	return DefaultRetentionDays * 24 * time.Hour
}

type Terraform struct {
	Binary  string   `yaml:"binary,omitempty"` // terraform | tofu
	Roots   []TFRoot `yaml:"roots,omitempty"`
	Modules []string `yaml:"modules,omitempty"`
	// Danger adds resource type patterns (globs, e.g. "*_cache_cluster") whose
	// destruction needs an explicit acknowledgement, on top of the built-ins.
	Danger []string `yaml:"danger,omitempty"`
	// PlanTTL is how long a saved plan stays approvable (default 1h).
	PlanTTL string `yaml:"plan_ttl,omitempty"`
	// RunTimeout bounds one run (plan or apply); a hung provider is killed
	// and the run fails instead of holding the root's lock forever (default 2h).
	RunTimeout string `yaml:"run_timeout,omitempty"`
}

// RunTimeoutDuration returns the configured run timeout (default two hours).
func (t Terraform) RunTimeoutDuration() time.Duration { return runTimeout(t.RunTimeout) }

func runTimeout(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return DefaultRunTimeout
}

// PlanTTLDuration returns the configured plan lifetime (default one hour).
func (t Terraform) PlanTTLDuration() time.Duration {
	if d, err := time.ParseDuration(t.PlanTTL); err == nil && d > 0 {
		return d
	}
	return time.Hour
}

type TFRoot struct {
	Path     string           `yaml:"path"`
	Env      string           `yaml:"env,omitempty"`
	Approval string           `yaml:"approval,omitempty"` // required | none
	Envs     map[string]TFEnv `yaml:"envs,omitempty"`
}

type TFEnv struct {
	Workspace string   `yaml:"workspace,omitempty"`
	VarFiles  []string `yaml:"var_files,omitempty"`
	Approval  string   `yaml:"approval,omitempty"`
}

type Ansible struct {
	Projects []AnsibleProject `yaml:"projects,omitempty"`
	// AllowVaultScript lets vault_password_file name an executable, which
	// ansible runs to obtain the password. Off by default: groundwork.yaml is
	// part of the repository, so a repository cloned through the UI could
	// otherwise run a script of its own choosing.
	AllowVaultScript bool `yaml:"allow_vault_script,omitempty"`
	// RunTimeout bounds one playbook or ad-hoc run (default 2h).
	RunTimeout string `yaml:"run_timeout,omitempty"`
}

// RunTimeoutDuration returns the configured run timeout (default two hours).
func (a Ansible) RunTimeoutDuration() time.Duration { return runTimeout(a.RunTimeout) }

type AnsibleProject struct {
	Path              string            `yaml:"path"`
	Config            string            `yaml:"config,omitempty"`
	Inventories       map[string]string `yaml:"inventories,omitempty"` // env → path relative to project
	VaultPasswordFile string            `yaml:"vault_password_file,omitempty"`
}

// VaultFile resolves p's vault password file to an absolute path ("" = none):
// a relative path is relative to the project, `~/` is the home directory. It
// refuses a file with the execute bit set unless ansible.allow_vault_script is
// on, since ansible executes such a file instead of reading it.
func (c *Config) VaultFile(repoRoot string, p AnsibleProject) (string, error) {
	v := p.VaultPasswordFile
	switch {
	case v == "":
		return "", nil
	case strings.HasPrefix(v, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		v = filepath.Join(home, v[2:])
	case filepath.IsAbs(v):
	default:
		v = filepath.Join(repoRoot, filepath.FromSlash(p.Path), filepath.FromSlash(v))
	}
	if c.Ansible.AllowVaultScript {
		return v, nil
	}
	if st, err := os.Stat(v); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
		return "", fmt.Errorf("vault_password_file %s is executable and ansible would run it; make it a plain file, or set ansible.allow_vault_script: true in groundwork.yaml if that is intended", p.VaultPasswordFile)
	}
	return v, nil
}

// envNameRe is what an environment or workspace name may look like: it goes
// into `terraform workspace select`, TF_WORKSPACE, URLs and inventory lookups.
var envNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidEnvName reports whether s is an acceptable environment/workspace name.
func ValidEnvName(s string) bool { return envNameRe.MatchString(s) }

type Checks struct {
	Enabled []string `yaml:"enabled,omitempty"`
	OnSave  bool     `yaml:"on_save"`
}

type Drift struct {
	Schedule string `yaml:"schedule,omitempty"`
	Notify   string `yaml:"notify,omitempty"` // desktop | webhook | none
	Webhook  string `yaml:"webhook,omitempty"`
}

var KnownChecks = []string{"fmt", "validate", "tflint", "checkov", "ansible-lint", "yamllint", "syntax-check", "secrets"}

// Default returns a config with defaults applied to unset fields.
func (c *Config) applyDefaults() {
	if c.Terraform.Binary == "" && (len(c.Terraform.Roots) > 0 || len(c.Terraform.Modules) > 0) {
		c.Terraform.Binary = "terraform"
	}
	if c.Drift.Notify == "" && c.Drift.Schedule != "" {
		c.Drift.Notify = "desktop"
	}
}

var prodLike = map[string]bool{"prod": true, "production": true, "prd": true, "live": true}

// IsProdLike reports whether an environment name implies approval by default.
func IsProdLike(env string) bool { return prodLike[strings.ToLower(env)] }

// ApprovalRequired resolves the approval policy for env under root r.
func (r TFRoot) ApprovalRequired(env string) bool {
	pol := r.Approval
	if e, ok := r.Envs[env]; ok && e.Approval != "" {
		pol = e.Approval
	}
	switch pol {
	case "required":
		return true
	case "none":
		return false
	}
	return IsProdLike(env)
}

// Problem is one validation finding with a source position (0 if unknown).
type Problem struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
	Msg  string `json:"message"`
}

type ValidationError struct {
	File     string
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	for i, p := range e.Problems {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.File)
		if p.Line > 0 {
			fmt.Fprintf(&b, ":%d:%d", p.Line, p.Col)
		}
		b.WriteString(": ")
		if p.Path != "" {
			b.WriteString(p.Path + ": ")
		}
		b.WriteString(p.Msg)
	}
	return b.String()
}

var (
	yamlLineRe        = regexp.MustCompile(`line (\d+): (.*)`)
	unknownTopLevelRe = regexp.MustCompile(`line (\d+): field (\S+) not found in type config\.Config$`)
)

// Parse decodes and validates groundwork.yaml content. name is used in errors.
func Parse(data []byte, name string) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, syntaxProblem(name, err)
	}
	if len(root.Content) == 0 {
		return nil, &ValidationError{name, []Problem{{Msg: "file is empty"}}}
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		var te *yaml.TypeError
		if errors.As(err, &te) {
			// yaml.v3 keeps decoding past a TypeError, so c is complete.
			// An unknown *top-level* key is tolerated with a warning: a
			// newer release may add one, and this file should still open
			// here. Unknown keys inside a known block are errors as before.
			ve := &ValidationError{File: name}
			for _, m := range te.Errors {
				if sm := unknownTopLevelRe.FindStringSubmatch(m); sm != nil {
					c.Warnings = append(c.Warnings, fmt.Sprintf("%s:%s: unknown setting %q is ignored (it may need a newer groundwork)", name, sm[1], sm[2]))
					continue
				}
				p := Problem{Msg: m}
				if sm := yamlLineRe.FindStringSubmatch(m); sm != nil {
					p.Line, _ = strconv.Atoi(sm[1])
					p.Col = 1
					p.Msg = sm[2]
				}
				ve.Problems = append(ve.Problems, p)
			}
			if len(ve.Problems) > 0 {
				return nil, ve
			}
		} else {
			return nil, syntaxProblem(name, err)
		}
	}
	c.applyDefaults()
	if probs := c.validate(); len(probs) > 0 {
		for i := range probs {
			if n := nodeAt(root.Content[0], probs[i].keys); n != nil {
				probs[i].Line, probs[i].Col = n.Line, n.Column
			}
		}
		ve := &ValidationError{File: name}
		for _, p := range probs {
			ve.Problems = append(ve.Problems, p.Problem)
		}
		return nil, ve
	}
	return &c, nil
}

func syntaxProblem(name string, err error) error {
	p := Problem{Msg: err.Error()}
	if sm := yamlLineRe.FindStringSubmatch(err.Error()); sm != nil {
		p.Line, _ = strconv.Atoi(sm[1])
		p.Col = 1
		p.Msg = sm[2]
	}
	return &ValidationError{name, []Problem{p}}
}

// Load reads and validates <repo>/groundwork.yaml. Returns os.ErrNotExist if absent.
func Load(repoRoot string) (*Config, error) {
	p := filepath.Join(repoRoot, FileName)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return Parse(b, FileName)
}

// Marshal renders the config as YAML with a header comment.
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# groundwork configuration. Edit freely; groundwork re-reads it on start.\n---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// ---- validation ----

type problem struct {
	Problem
	keys []any
}

func (c *Config) validate() []problem {
	var out []problem
	add := func(msg string, keys ...any) {
		out = append(out, problem{Problem{Path: pathString(keys), Msg: msg}, keys})
	}
	if c.Version != 1 {
		add(fmt.Sprintf("unsupported version %d (want 1)", c.Version), "version")
	}
	if c.Mode != "standalone" && c.Mode != "embedded" {
		add(fmt.Sprintf("must be \"standalone\" or \"embedded\", got %q", c.Mode), "mode")
	}
	if b := c.Terraform.Binary; b != "" && b != "terraform" && b != "tofu" {
		add(fmt.Sprintf("must be \"terraform\" or \"tofu\", got %q", b), "terraform", "binary")
	}
	seen := map[string]bool{}
	for i, r := range c.Terraform.Roots {
		if msg := checkRelPath(r.Path); msg != "" {
			add(msg, "terraform", "roots", i, "path")
		} else if seen[path.Clean(r.Path)] {
			add("duplicate root "+r.Path, "terraform", "roots", i, "path")
		}
		seen[path.Clean(r.Path)] = true
		if r.Env != "" && len(r.Envs) > 0 {
			add("set either env or envs, not both", "terraform", "roots", i)
		}
		if r.Env == "" && len(r.Envs) == 0 {
			add("needs env (directory-per-env) or envs (workspaces)", "terraform", "roots", i)
		}
		if !validApproval(r.Approval) {
			add("approval must be required or none", "terraform", "roots", i, "approval")
		}
		if r.Env != "" && !ValidEnvName(r.Env) {
			add("environment names may contain letters, digits, '_', '.' and '-'", "terraform", "roots", i, "env")
		}
		for name, e := range r.Envs {
			if !ValidEnvName(name) {
				add("environment names may contain letters, digits, '_', '.' and '-'", "terraform", "roots", i, "envs", name)
			}
			if e.Workspace != "" && !ValidEnvName(e.Workspace) {
				add("workspace names may contain letters, digits, '_', '.' and '-'", "terraform", "roots", i, "envs", name, "workspace")
			}
			if !validApproval(e.Approval) {
				add("approval must be required or none", "terraform", "roots", i, "envs", name, "approval")
			}
			for j, vf := range e.VarFiles {
				if msg := checkRelPath(vf); msg != "" {
					add(msg, "terraform", "roots", i, "envs", name, "var_files", j)
				}
			}
		}
	}
	if c.Terraform.PlanTTL != "" {
		if d, err := time.ParseDuration(c.Terraform.PlanTTL); err != nil || d <= 0 {
			add("must be a positive duration such as 30m or 2h", "terraform", "plan_ttl")
		}
	}
	if c.Terraform.RunTimeout != "" {
		if d, err := time.ParseDuration(c.Terraform.RunTimeout); err != nil || d <= 0 {
			add("must be a positive duration such as 30m or 2h", "terraform", "run_timeout")
		}
	}
	if c.Ansible.RunTimeout != "" {
		if d, err := time.ParseDuration(c.Ansible.RunTimeout); err != nil || d <= 0 {
			add("must be a positive duration such as 30m or 2h", "ansible", "run_timeout")
		}
	}
	if c.Retention.Runs < 0 {
		add("must be a positive number of runs", "retention", "runs")
	}
	if c.Retention.Days < 0 {
		add("must be a positive number of days", "retention", "days")
	}
	for i, p := range c.Terraform.Danger {
		if _, err := path.Match(p, "x"); err != nil || p == "" {
			add("invalid glob pattern", "terraform", "danger", i)
		}
	}
	for i, m := range c.Terraform.Modules {
		if msg := checkRelPath(strings.TrimSuffix(m, "/*")); msg != "" {
			add(msg, "terraform", "modules", i)
		}
	}
	for i, p := range c.Ansible.Projects {
		if msg := checkRelPath(p.Path); msg != "" {
			add(msg, "ansible", "projects", i, "path")
		}
		if p.Config != "" {
			if msg := checkRelPath(p.Config); msg != "" {
				add(msg, "ansible", "projects", i, "config")
			}
		}
		if msg := CheckVaultPath(p.VaultPasswordFile); msg != "" {
			add(msg, "ansible", "projects", i, "vault_password_file")
		}
		for env, inv := range p.Inventories {
			if !ValidEnvName(env) {
				add("environment names may contain letters, digits, '_', '.' and '-'", "ansible", "projects", i, "inventories", env)
			}
			if msg := checkRelPath(inv); msg != "" {
				add(msg, "ansible", "projects", i, "inventories", env)
			}
		}
	}
	for i, p := range c.Ignore {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, "\n\x00") {
			add("must be a single .gitignore-style pattern", "ignore", i)
		}
	}
	for i, ch := range c.Checks.Enabled {
		if !contains(KnownChecks, ch) {
			add(fmt.Sprintf("unknown check %q (known: %s)", ch, strings.Join(KnownChecks, ", ")), "checks", "enabled", i)
		}
	}
	switch c.Drift.Notify {
	case "", "desktop", "none":
	case "webhook":
		if c.Drift.Webhook == "" {
			add("webhook URL required when notify is webhook", "drift", "webhook")
		}
	default:
		add("notify must be desktop, webhook or none", "drift", "notify")
	}
	if c.Drift.Webhook != "" {
		if msg := checkWebhook(c.Drift.Webhook); msg != "" {
			add(msg, "drift", "webhook")
		}
	}
	if s := c.Drift.Schedule; s != "" && len(strings.Fields(s)) != 5 {
		add("schedule must be a 5-field cron expression", "drift", "schedule")
	}
	for _, p := range c.Notify.problems(c.Drift.Webhook, false) {
		add(p.msg, append([]any{"notify"}, p.keys...)...)
	}
	return out
}

func validApproval(a string) bool { return a == "" || a == "required" || a == "none" }

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// CheckVaultPath allows a vault password file relative to the project, under
// ~/ or absolute, as long as it never climbs with "..". The execute-bit rule
// needs the filesystem and lives in Config.VaultFile. It returns "" when
// p is fine (or empty), else what is wrong with it.
func CheckVaultPath(p string) string {
	if p == "" {
		return ""
	}
	if strings.ContainsRune(p, 0) {
		return "invalid path"
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return "must not contain \"..\""
		}
	}
	if strings.HasPrefix(p, "~") && !strings.HasPrefix(p, "~/") {
		return "only ~/ is expanded"
	}
	return ""
}

// checkWebhook accepts an absolute http(s) URL with a host.
func checkWebhook(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "invalid URL"
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "must be an http:// or https:// URL"
	}
	if u.Host == "" || u.User != nil {
		return "must have a host and no credentials"
	}
	return ""
}

// checkRelPath returns a message if p is not a clean repo-relative path.
func checkRelPath(p string) string {
	switch {
	case p == "":
		return "path is required"
	case strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || filepath.IsAbs(p):
		return "must be relative to the repository root"
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "must stay inside the repository"
	}
	return ""
}

func pathString(keys []any) string {
	var b strings.Builder
	for _, k := range keys {
		switch v := k.(type) {
		case int:
			fmt.Fprintf(&b, "[%d]", v)
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(v)
		}
	}
	return b.String()
}

// nodeAt finds the YAML node at the key path (string keys, int indexes).
// For mapping lookups it returns the key node so positions point at the field.
func nodeAt(n *yaml.Node, keys []any) *yaml.Node {
	last := n
	for _, k := range keys {
		switch v := k.(type) {
		case string:
			if n.Kind != yaml.MappingNode {
				return last
			}
			found := false
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == v {
					last, n, found = n.Content[i], n.Content[i+1], true
					break
				}
			}
			if !found {
				return last
			}
		case int:
			if n.Kind != yaml.SequenceNode || v >= len(n.Content) {
				return last
			}
			last, n = n.Content[v], n.Content[v]
		}
	}
	return last
}
