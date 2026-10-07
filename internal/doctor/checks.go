package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/store"
)

// Check is one Doctor result.
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail | missing
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Report is a full Doctor run.
type Report struct {
	RanAt  time.Time `json:"ran_at"`
	Checks []Check   `json:"checks"`
}

// Exec runs external commands; tests replace it.
type Exec interface {
	LookPath(string) (string, error)
	Run(ctx context.Context, dir string, argv ...string) (stdout, stderr string, code int, err error)
}

type OSExec struct{}

func (OSExec) LookPath(n string) (string, error) { return exec.LookPath(n) }
func (OSExec) Run(ctx context.Context, dir string, argv ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if x, ok := err.(*exec.ExitError); ok {
		code, err = x.ExitCode(), nil
	}
	return o.String(), e.String(), code, err
}

// Backend is a root's state backend, read from its terraform block.
type Backend struct {
	Root  string
	Type  string
	Attrs map[string]string // literal string/bool attributes
}

// Backends reads `terraform { backend "x" { ... } }` from each root.
func Backends(repoRoot string, roots []string) []Backend {
	var out []Backend
	for _, r := range roots {
		files, _ := filepath.Glob(filepath.Join(repoRoot, filepath.FromSlash(r), "*.tf"))
		b := Backend{Root: r, Type: "local", Attrs: map[string]string{}}
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			file, diags := hclsyntax.ParseConfig(src, f, hcl.InitialPos)
			if diags.HasErrors() {
				continue
			}
			for _, blk := range file.Body.(*hclsyntax.Body).Blocks {
				if blk.Type != "terraform" {
					continue
				}
				for _, ib := range blk.Body.Blocks {
					if ib.Type == "cloud" {
						b.Type = "cloud"
					}
					if ib.Type != "backend" || len(ib.Labels) == 0 {
						continue
					}
					b.Type = ib.Labels[0]
					for name, attr := range ib.Body.Attributes {
						if v, d := attr.Expr.Value(nil); !d.HasErrors() && v.IsKnown() && !v.IsNull() {
							switch v.Type().FriendlyName() {
							case "string":
								b.Attrs[name] = v.AsString()
							case "bool":
								b.Attrs[name] = fmt.Sprint(v.True())
							}
						}
					}
				}
			}
		}
		out = append(out, b)
	}
	return out
}

// Input is what Doctor looks at.
type Input struct {
	Root      string
	Cfg       *config.Config
	Providers []string // provider names used anywhere (aws, google, azurerm, …)
	Store     *store.Store
	Exec      Exec
	Home      string
}

type checkFn func(ctx context.Context, in Input) []Check

// Run runs every check in parallel, each with its own timeout.
func Run(ctx context.Context, in Input) Report {
	if in.Exec == nil {
		in.Exec = OSExec{}
	}
	if in.Home == "" {
		in.Home, _ = os.UserHomeDir()
	}
	fns := []checkFn{checkTools, checkAWS, checkGCP, checkAzure, checkBackends, checkSSHAgent, checkInventory, checkVault, checkDisk, checkGit}
	results := make([][]Check, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			results[i] = fn(cctx, in)
		}()
	}
	wg.Wait()
	rep := Report{RanAt: time.Now().UTC()}
	for _, r := range results {
		rep.Checks = append(rep.Checks, r...)
	}
	return rep
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func has(list []string, x string) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}

func checkTools(ctx context.Context, in Input) []Check {
	need := map[string]string{} // tool → why
	if in.Cfg != nil {
		if len(in.Cfg.Terraform.Roots) > 0 {
			bin := in.Cfg.Terraform.Binary
			if bin == "" {
				bin = "terraform"
			}
			need[bin] = "required"
		}
		if len(in.Cfg.Ansible.Projects) > 0 {
			need["ansible"] = "required"
		}
		for _, c := range in.Cfg.Checks.Enabled {
			switch c {
			case "tflint", "checkov", "yamllint", "ansible-lint":
				need[c] = "check"
			}
		}
	}
	need["git"] = "optional"
	var names []string
	for n := range need {
		names = append(names, n)
	}
	sort.Strings(names)
	var found []string
	var out []Check
	for _, n := range names {
		var spec *struct {
			name string
			args []string
			re   string
			hint string
		}
		for i := range specs {
			if specs[i].name == n {
				spec = &specs[i]
			}
		}
		if _, err := in.Exec.LookPath(n); err != nil {
			st := "fail"
			if need[n] != "required" {
				st = "warn"
			}
			c := Check{ID: "tool:" + n, Name: n, Status: st, Detail: "not found on PATH"}
			if spec != nil {
				c.Hint = spec.hint
			}
			if need[n] == "check" {
				c.Detail += "; the " + n + " check is skipped"
			}
			out = append(out, c)
			continue
		}
		ver := ""
		if spec != nil {
			o, e, _, _ := in.Exec.Run(ctx, in.Root, append([]string{n}, spec.args...)...)
			if m := regexp.MustCompile(spec.re).FindStringSubmatch(o + e); m != nil {
				ver = " " + m[1]
			}
		}
		found = append(found, n+ver)
	}
	if len(found) > 0 {
		out = append([]Check{{ID: "tools", Name: "Tools", Status: "ok", Detail: strings.Join(found, " · ")}}, out...)
	}
	return out
}

// awsSSOExpiry finds the newest SSO token's expiry in ~/.aws/sso/cache.
func awsSSOExpiry(home string) (time.Time, bool) {
	files, _ := filepath.Glob(filepath.Join(home, ".aws", "sso", "cache", "*.json"))
	var best time.Time
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var tok struct {
			ExpiresAt   string `json:"expiresAt"`
			AccessToken string `json:"accessToken"`
		}
		if json.Unmarshal(b, &tok) != nil || tok.AccessToken == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, tok.ExpiresAt); err == nil && t.After(best) {
			best = t
		}
	}
	return best, !best.IsZero()
}

func humanDur(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func checkAWS(ctx context.Context, in Input) []Check {
	if !has(in.Providers, "aws") {
		return nil
	}
	c := Check{ID: "cred:aws", Name: "AWS credentials"}
	profile := os.Getenv("AWS_PROFILE")
	label := "default profile"
	if profile != "" {
		label = "profile " + profile
	}
	if _, err := in.Exec.LookPath("aws"); err != nil {
		if os.Getenv("AWS_ACCESS_KEY_ID") != "" || profile != "" || fileExists(filepath.Join(in.Home, ".aws", "credentials")) || fileExists(filepath.Join(in.Home, ".aws", "config")) {
			c.Status, c.Detail, c.Hint = "warn", "credentials are configured ("+label+") but can't be verified without the aws CLI", "brew install awscli"
		} else {
			c.Status, c.Detail, c.Hint = "fail", "no AWS credentials in the environment or ~/.aws", "aws configure sso"
		}
		return []Check{c}
	}
	o, e, code, err := in.Exec.Run(ctx, in.Root, "aws", "sts", "get-caller-identity", "--output", "json")
	if err != nil || code != 0 {
		msg := firstLine(e + o)
		if err != nil {
			msg = err.Error()
		}
		c.Status, c.Detail = "fail", msg
		if strings.Contains(msg, "SSO") || strings.Contains(msg, "expired") || strings.Contains(msg, "Token") {
			c.Hint = "aws sso login" + map[bool]string{true: " --profile " + profile, false: ""}[profile != ""]
		} else {
			c.Hint = "aws configure sso"
		}
		return []Check{c}
	}
	var id struct{ Account, Arn string }
	_ = json.Unmarshal([]byte(o), &id)
	c.Status, c.Detail = "ok", fmt.Sprintf("%s · account %s", label, id.Account)
	if exp, ok := awsSSOExpiry(in.Home); ok {
		left := time.Until(exp)
		switch {
		case left <= 0:
		case left < 15*time.Minute:
			c.Status = "warn"
			c.Detail += " · SSO session expires in " + humanDur(left)
			c.Hint = "aws sso login"
		default:
			c.Detail += " · SSO session expires in " + humanDur(left)
		}
	}
	return []Check{c}
}

func checkGCP(ctx context.Context, in Input) []Check {
	if !has(in.Providers, "google") && !has(in.Providers, "google-beta") {
		return nil
	}
	c := Check{ID: "cred:gcp", Name: "Google Cloud credentials"}
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		c.Status, c.Detail = "ok", "GOOGLE_APPLICATION_CREDENTIALS is set"
		return []Check{c}
	}
	if _, err := in.Exec.LookPath("gcloud"); err != nil {
		c.Status, c.Detail, c.Hint = "warn", "gcloud isn't installed and GOOGLE_APPLICATION_CREDENTIALS isn't set", "gcloud auth application-default login"
		return []Check{c}
	}
	o, e, code, err := in.Exec.Run(ctx, in.Root, "gcloud", "auth", "application-default", "print-access-token")
	if err != nil || code != 0 || strings.TrimSpace(o) == "" {
		c.Status, c.Detail, c.Hint = "fail", firstLine(e), "gcloud auth application-default login"
		return []Check{c}
	}
	c.Status, c.Detail = "ok", "application-default credentials work"
	return []Check{c}
}

func checkAzure(ctx context.Context, in Input) []Check {
	if !has(in.Providers, "azurerm") {
		return nil
	}
	c := Check{ID: "cred:azure", Name: "Azure credentials"}
	if os.Getenv("ARM_CLIENT_ID") != "" {
		c.Status, c.Detail = "ok", "service principal from ARM_CLIENT_ID"
		return []Check{c}
	}
	if _, err := in.Exec.LookPath("az"); err != nil {
		c.Status, c.Detail, c.Hint = "warn", "az isn't installed and ARM_CLIENT_ID isn't set", "az login"
		return []Check{c}
	}
	o, e, code, err := in.Exec.Run(ctx, in.Root, "az", "account", "show", "--output", "json")
	if err != nil || code != 0 {
		c.Status, c.Detail, c.Hint = "fail", firstLine(e), "az login"
		return []Check{c}
	}
	var acct struct{ Name string }
	_ = json.Unmarshal([]byte(o), &acct)
	c.Status, c.Detail = "ok", "subscription "+acct.Name
	return []Check{c}
}

func checkBackends(ctx context.Context, in Input) []Check {
	if in.Cfg == nil {
		return nil
	}
	var roots []string
	for _, r := range in.Cfg.Terraform.Roots {
		roots = append(roots, r.Path)
	}
	var out []Check
	seen := map[string]bool{}
	_, awsErr := in.Exec.LookPath("aws")
	for _, b := range Backends(in.Root, roots) {
		switch b.Type {
		case "s3":
			bucket := b.Attrs["bucket"]
			if bucket == "" || seen["s3:"+bucket] {
				continue
			}
			seen["s3:"+bucket] = true
			c := Check{ID: "backend:s3:" + bucket, Name: "State backend"}
			if awsErr != nil {
				c.Status, c.Detail = "warn", "s3://"+bucket+" (not checked: aws CLI not installed)"
			} else if _, e, code, err := in.Exec.Run(ctx, in.Root, "aws", "s3api", "head-bucket", "--bucket", bucket); err != nil || code != 0 {
				c.Status, c.Detail, c.Hint = "fail", "s3://"+bucket+": "+firstLine(e), "check the bucket name and that your role can s3:ListBucket"
			} else {
				c.Status, c.Detail = "ok", "s3://"+bucket+" reachable"
			}
			out = append(out, c)
			lc := Check{ID: "lock:s3:" + bucket, Name: "State locking"}
			switch {
			case b.Attrs["use_lockfile"] == "true":
				lc.Status, lc.Detail = "ok", "S3 native lock file"
			case b.Attrs["dynamodb_table"] != "":
				t := b.Attrs["dynamodb_table"]
				if awsErr != nil {
					lc.Status, lc.Detail = "warn", "dynamodb "+t+" (not checked)"
				} else if _, e, code, err := in.Exec.Run(ctx, in.Root, "aws", "dynamodb", "describe-table", "--table-name", t, "--output", "json"); err != nil || code != 0 {
					lc.Status, lc.Detail = "fail", "dynamodb "+t+": "+firstLine(e)
				} else {
					lc.Status, lc.Detail = "ok", "dynamodb "+t+" exists"
				}
			default:
				lc.Status, lc.Detail, lc.Hint = "warn", "no locking configured for s3://"+bucket, "set use_lockfile = true in the backend block"
			}
			out = append(out, lc)
		case "local":
			if !seen["local"] {
				seen["local"] = true
				out = append(out, Check{ID: "backend:local", Name: "State backend", Status: "ok", Detail: "local state files (fine for one person; use a remote backend to share)"})
			}
		default:
			if !seen[b.Type] {
				seen[b.Type] = true
				out = append(out, Check{ID: "backend:" + b.Type, Name: "State backend", Status: "warn", Detail: b.Type + " backend (groundwork can't check it yet)"})
			}
		}
	}
	return out
}

func checkSSHAgent(ctx context.Context, in Input) []Check {
	if in.Cfg == nil || len(in.Cfg.Ansible.Projects) == 0 {
		return nil
	}
	c := Check{ID: "ssh-agent", Name: "SSH agent"}
	if _, err := in.Exec.LookPath("ssh-add"); err != nil {
		c.Status, c.Detail = "warn", "ssh-add not found"
		return []Check{c}
	}
	o, _, code, err := in.Exec.Run(ctx, in.Root, "ssh-add", "-l")
	switch {
	case err != nil:
		c.Status, c.Detail = "warn", err.Error()
	case code == 2:
		c.Status, c.Detail, c.Hint = "warn", "no SSH agent is running (keys must be unencrypted or in ~/.ssh/config)", "eval \"$(ssh-agent)\" && ssh-add"
	case code == 1:
		c.Status, c.Detail, c.Hint = "warn", "the agent has no keys loaded", "ssh-add ~/.ssh/id_ed25519"
	default:
		var types []string
		n := 0
		for _, l := range strings.Split(strings.TrimSpace(o), "\n") {
			if l == "" {
				continue
			}
			n++
			if i := strings.LastIndex(l, "("); i >= 0 {
				types = append(types, strings.ToLower(strings.Trim(l[i:], "()")))
			}
		}
		c.Status, c.Detail = "ok", fmt.Sprintf("%d key%s loaded · %s", n, map[bool]string{true: "", false: "s"}[n == 1], strings.Join(types, ", "))
	}
	return []Check{c}
}

func checkInventory(_ context.Context, in Input) []Check {
	if in.Cfg == nil || in.Store == nil {
		return nil
	}
	var out []Check
	for _, p := range in.Cfg.Ansible.Projects {
		envs := []string{}
		for e := range p.Inventories {
			envs = append(envs, e)
		}
		if len(envs) == 0 {
			envs = []string{"default"}
		}
		sort.Strings(envs)
		for _, env := range envs {
			proj := filepath.ToSlash(filepath.Clean(p.Path))
			st, err := in.Store.HostStatuses(proj + "#" + env)
			c := Check{ID: "reach:" + proj + "#" + env, Name: "Inventory reachability · " + env}
			if err != nil || len(st) == 0 {
				c.Status, c.Detail, c.Hint = "warn", "never pinged", "Ping all from the Inventory screen"
				out = append(out, c)
				continue
			}
			up := 0
			var down []string
			for h, s := range st {
				if s.Reachable {
					up++
				} else {
					down = append(down, h)
				}
			}
			sort.Strings(down)
			c.Detail = fmt.Sprintf("%d of %d hosts answer ping", up, len(st))
			c.Status = "ok"
			if len(down) > 0 {
				c.Status = "warn"
				c.Detail += " · down: " + strings.Join(down, ", ")
			}
			out = append(out, c)
		}
	}
	return out
}

// usesVault reports whether a project contains vault-encrypted data.
func usesVault(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == ".groundwork" || n == "node_modules" || n == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext != ".yml" && ext != ".yaml" && ext != "" {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 1<<20 {
			return nil
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte("$ANSIBLE_VAULT;")) {
			found = true
		}
		return nil
	})
	return found
}

func checkVault(_ context.Context, in Input) []Check {
	if in.Cfg == nil {
		return nil
	}
	var out []Check
	for _, p := range in.Cfg.Ansible.Projects {
		dir := filepath.Join(in.Root, filepath.FromSlash(p.Path))
		if !usesVault(dir) {
			continue
		}
		c := Check{ID: "vault:" + p.Path, Name: "Vault password file"}
		pw := p.VaultPasswordFile
		if pw == "" {
			if os.Getenv("ANSIBLE_VAULT_PASSWORD_FILE") != "" {
				c.Status, c.Detail = "ok", "from ANSIBLE_VAULT_PASSWORD_FILE"
			} else {
				c.Status, c.Detail, c.Hint = "fail", p.Path+" uses ansible-vault but no vault_password_file is set", "add vault_password_file to the project in groundwork.yaml"
			}
			out = append(out, c)
			continue
		}
		if strings.HasPrefix(pw, "~/") {
			pw = filepath.Join(in.Home, pw[2:])
		} else if !filepath.IsAbs(pw) {
			pw = filepath.Join(dir, pw)
		}
		info, err := os.Stat(pw)
		switch {
		case err != nil:
			c.Status, c.Detail, c.Hint = "fail", p.VaultPasswordFile+" doesn't exist", "create it, or fix vault_password_file in groundwork.yaml"
		case info.Mode().Perm()&0o077 != 0 && info.Mode().Perm()&0o111 == 0:
			c.Status, c.Detail, c.Hint = "warn", p.VaultPasswordFile+" is readable by other users", "chmod 600 "+p.VaultPasswordFile
		default:
			c.Status, c.Detail = "ok", p.VaultPasswordFile+" present"
		}
		out = append(out, c)
	}
	return out
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func bytesH(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n/1024)
}

func checkDisk(_ context.Context, in Input) []Check {
	dir := filepath.Join(in.Root, ".groundwork")
	var fs syscall.Statfs_t
	if err := syscall.Statfs(in.Root, &fs); err != nil {
		return []Check{{ID: "disk", Name: "Disk space", Status: "warn", Detail: err.Error()}}
	}
	free := int64(fs.Bavail) * int64(fs.Bsize)
	c := Check{ID: "disk", Name: "Disk space", Status: "ok", Detail: fmt.Sprintf(".groundwork uses %s · %s free", bytesH(dirSize(dir)), bytesH(free))}
	if free < 1<<30 {
		c.Status, c.Hint = "warn", "free some space: provider downloads and plan files need room"
	}
	return []Check{c}
}

func checkGit(ctx context.Context, in Input) []Check {
	if _, err := os.Stat(filepath.Join(in.Root, ".git")); err != nil {
		return []Check{{ID: "git", Name: "Git repository", Status: "warn", Detail: "not a git repository: runs can't record the commit they used", Hint: "git init"}}
	}
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
