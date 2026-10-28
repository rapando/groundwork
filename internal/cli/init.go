package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/rapando/groundwork/v2/internal/app"
	"github.com/rapando/groundwork/v2/internal/config"
	"github.com/rapando/groundwork/v2/internal/scaffold"
	"github.com/rapando/groundwork/v2/internal/workspace"
)

type initFlags struct {
	project, layout, vaultPasswordFile string
	envs, checks, tools                []string
	detect, dryRun, yes, gitCommit     bool
}

func newInit() *cobra.Command {
	var f initFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect existing IaC, or set up a new repository",
		Long: `In a repository with Terraform or Ansible, init detects them and writes
groundwork.yaml. In one without, it sets up only the --tools you choose:
groundwork.yaml, plus ansible.cfg for Ansible, plus the inventory script that
reads your Terraform's ansible_hosts output when you use both. You write the
Terraform and the playbooks yourself.
Nothing is written until you confirm (or pass --yes); --dry-run writes nothing.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := app.FindRoot(".")
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(root, config.FileName)); err == nil {
				return fmt.Errorf("%s already exists in %s", config.FileName, root)
			}
			in := bufio.NewReader(cmd.InOrStdin())
			if !f.detect {
				rep, err := workspace.Detect(root, nil)
				if err != nil {
					return err
				}
				if !rep.HasIaC() {
					return runSetup(cmd.OutOrStdout(), in, root, &f)
				}
			}
			return runDetect(cmd.OutOrStdout(), in, root, &f)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.detect, "detect", false, "only detect existing IaC; never set up a new repository")
	fl.BoolVar(&f.dryRun, "dry-run", false, "show what would be written, write nothing")
	fl.BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation")
	fl.StringVar(&f.project, "project", "", "project name (default: directory name)")
	fl.StringSliceVar(&f.tools, "tools", nil, "what the repository uses: terraform or tofu, and/or ansible (default: terraform,ansible, or tofu,ansible when only tofu is installed)")
	fl.StringSliceVar(&f.envs, "envs", nil, "environments (default: dev,staging,prod)")
	fl.StringVar(&f.layout, "layout", "dirs", "where your Terraform will live: dirs (terraform/envs/<env>) or workspaces (terraform, one workspace per env)")
	fl.StringSliceVar(&f.checks, "checks", []string{"fmt", "validate", "tflint", "ansible-lint", "yamllint"}, "checks to configure")
	fl.StringVar(&f.vaultPasswordFile, "vault-password-file", "", "ansible-vault password file outside the repository, e.g. ~/.vault-pass; created with the password you type if it doesn't exist (default: none, type the password in the console)")
	fl.BoolVar(&f.gitCommit, "git-commit", false, "commit the files written")
	return cmd
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		s = "infra"
	}
	return s
}

func confirm(out io.Writer, in *bufio.Reader, q string) bool {
	fmt.Fprintf(out, "%s [y/N] ", q)
	line, _ := in.ReadString('\n')
	l := strings.ToLower(strings.TrimSpace(line))
	return l == "y" || l == "yes"
}

func runSetup(out io.Writer, in *bufio.Reader, root string, f *initFlags) error {
	form := scaffold.Form{Project: f.project, Tools: f.tools, Envs: f.envs, Layout: f.layout, VaultPasswordFile: f.vaultPasswordFile, Checks: f.checks}
	if form.Project == "" {
		form.Project = slug(filepath.Base(root))
	}
	if form.Tools == nil {
		form.Tools = scaffold.DefaultTools(exec.LookPath)
	}
	form.Normalize()
	if errs := form.Validate(); len(errs) > 0 {
		var b strings.Builder
		for _, e := range errs {
			fmt.Fprintf(&b, "  --%s: %s\n", strings.ReplaceAll(e.Field, "_", "-"), e.Message)
		}
		return errors.New("invalid options:\n" + strings.TrimRight(b.String(), "\n"))
	}
	vf, err := scaffold.PasswordFile(root, &form)
	if err != nil {
		return fmt.Errorf("--vault-password-file: %w", err)
	}
	files, err := scaffold.Render(&form)
	if err != nil {
		return err
	}
	planned, err := scaffold.Plan(root, files)
	if err != nil {
		return err
	}
	existing := 0
	for _, p := range planned {
		mark := "+"
		if p.Exists {
			mark, existing = "~", existing+1
		}
		fmt.Fprintf(out, "  %s %s\n", mark, p.Path)
	}
	if vf != nil && !vf.Exists {
		fmt.Fprintf(out, "  + %s (the vault password, mode 600, outside the repository)\n", vf.Path)
	}
	fmt.Fprintf(out, "%d files (%d already exist and will be kept)\n", len(planned), existing)
	switch {
	case form.IaC() != "" && form.Ansible():
		fmt.Fprintln(out, "Write your Terraform under terraform/ with an ansible_hosts output; the inventory script reads it.")
	case form.IaC() != "":
		fmt.Fprintln(out, "Write your Terraform under terraform/.")
	default:
		fmt.Fprintln(out, "Write your inventory and playbooks under ansible/.")
	}
	if f.dryRun {
		fmt.Fprintln(out, "dry run: nothing written")
		return nil
	}
	if !f.yes && !confirm(out, in, "Write these files?") {
		fmt.Fprintln(out, "aborted")
		return nil
	}
	if vf != nil && !vf.Exists {
		pw, err := newVaultPassword(out, in)
		if err != nil {
			return err
		}
		if err := vf.Create(pw); err != nil {
			return err
		}
		fmt.Fprintf(out, "created %s\n", vf.Path)
	}
	res, err := scaffold.Apply(root, files, nil)
	if err != nil {
		return err
	}
	summarise(out, res)
	if vf == nil && form.Ansible() {
		fmt.Fprintln(out, "No vault password file: type the password in the console, Variables → Secrets → Unlock.")
	}
	if f.gitCommit {
		return commitScaffold(out, root, res)
	}
	return nil
}

// newVaultPassword asks for the password twice, hidden on a terminal.
func newVaultPassword(out io.Writer, in *bufio.Reader) (string, error) {
	read := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		if in.Buffered() == 0 && term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(out)
			return string(b), err
		}
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no vault password given")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	pw, err := read("ansible-vault password: ")
	if err != nil {
		return "", err
	}
	if err := scaffold.CheckPassword(pw); err != nil {
		return "", err
	}
	again, err := read("Repeat it: ")
	if err != nil {
		return "", err
	}
	if again != pw {
		return "", errors.New("the passwords don't match; nothing was written")
	}
	return pw, nil
}

func runDetect(out io.Writer, in *bufio.Reader, root string, f *initFlags) error {
	rep, err := workspace.Detect(root, nil)
	if err != nil {
		return err
	}
	if !rep.HasIaC() {
		return errors.New("no Terraform or Ansible found here; run init without --detect to set up a new repository")
	}
	fmt.Fprintf(out, "mode: %s\n", rep.Mode)
	for _, d := range rep.TerraformRoots {
		fmt.Fprintf(out, "  terraform root    %s\n", d.Path)
	}
	for _, d := range rep.TerraformModules {
		fmt.Fprintf(out, "  terraform module  %s\n", d.Path)
	}
	for _, p := range rep.Ansible {
		fmt.Fprintf(out, "  ansible project   %s (%d playbooks, %d roles, %d inventories)\n",
			p.Path, len(p.Playbooks), len(p.Roles), len(p.Inventories))
	}
	cfg := config.FromReport(rep, config.DefaultTFBinary(exec.LookPath))
	y, err := cfg.Marshal()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%s:\n%s\n", config.FileName, indent(string(y)))
	if f.dryRun {
		fmt.Fprintln(out, "dry run: nothing written")
		return nil
	}
	if !f.yes && !confirm(out, in, "Write "+config.FileName+"?") {
		fmt.Fprintln(out, "aborted")
		return nil
	}
	res, err := scaffold.Apply(root, []scaffold.File{{Path: config.FileName, Content: string(y)}}, nil)
	if err != nil {
		return err
	}
	summarise(out, res)
	return nil
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}

func summarise(out io.Writer, res []scaffold.Result) {
	n := map[string]int{}
	for _, r := range res {
		n[r.Status]++
	}
	fmt.Fprintf(out, "created %d, merged %d, skipped %d, overwritten %d\n", n["created"], n["merged"], n["skipped"], n["overwritten"])
	fmt.Fprintln(out, "Run `groundwork` to open the console.")
}
