package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/internal/app"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/scaffold"
	"github.com/rapando/groundwork/internal/workspace"
)

type initFlags struct {
	preset, project, backend, bucket, region, layout, inventory string
	envs, roles, checks                                         []string
	vault, noPreCommit, noCI, detect, dryRun, yes, gitCommit    bool
}

func newInit() *cobra.Command {
	var f initFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect existing IaC or scaffold a new layout",
		Long: `Without --preset, init detects Terraform and Ansible in this repository and
writes groundwork.yaml. With --preset it scaffolds a fresh layout.
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
			if f.preset != "" && !f.detect {
				return runScaffold(cmd.OutOrStdout(), in, root, &f)
			}
			return runDetect(cmd.OutOrStdout(), in, root, &f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.preset, "preset", "", "scaffold a layout: "+strings.Join(scaffold.Presets, "|"))
	fl.BoolVar(&f.detect, "detect", false, "detect existing IaC (default when no --preset)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "show what would be written, write nothing")
	fl.BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation")
	fl.StringVar(&f.project, "project", "", "project name (default: directory name)")
	fl.StringVar(&f.backend, "backend", "", "state backend: s3|local|http")
	fl.StringVar(&f.bucket, "bucket", "", "S3 state bucket (default: <project>-tfstate)")
	fl.StringVar(&f.region, "region", "", "AWS region, e.g. eu-west-1")
	fl.StringSliceVar(&f.envs, "envs", nil, "environments (default: dev,staging,prod)")
	fl.StringVar(&f.layout, "layout", "dirs", "dirs|workspaces")
	fl.StringVar(&f.inventory, "inventory", "terraform", "terraform|static")
	fl.StringSliceVar(&f.roles, "roles", []string{"common"}, "starter Ansible roles")
	fl.StringSliceVar(&f.checks, "checks", []string{"fmt", "validate", "tflint", "ansible-lint", "yamllint"}, "checks to configure")
	fl.BoolVar(&f.vault, "vault", false, "add ansible-vault example files")
	fl.BoolVar(&f.noPreCommit, "no-pre-commit", false, "skip .pre-commit-config.yaml")
	fl.BoolVar(&f.noCI, "no-ci", false, "skip the GitHub Actions workflow")
	fl.BoolVar(&f.gitCommit, "git-commit", false, "commit the scaffolded files")
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

func runScaffold(out io.Writer, in *bufio.Reader, root string, f *initFlags) error {
	form := scaffold.Form{
		Preset: f.preset, Project: f.project, Backend: f.backend, Bucket: f.bucket, Region: f.region,
		Envs: f.envs, Layout: f.layout, Inventory: f.inventory, Roles: f.roles, Vault: f.vault,
		Checks: f.checks, PreCommit: !f.noPreCommit, CI: !f.noCI,
	}
	if form.Project == "" {
		form.Project = slug(filepath.Base(root))
	}
	form.Normalize()
	if form.Bucket == "" && form.Backend == "s3" {
		form.Bucket = form.Project + "-tfstate"
	}
	if errs := form.Validate(); len(errs) > 0 {
		var b strings.Builder
		for _, e := range errs {
			fmt.Fprintf(&b, "  --%s: %s\n", e.Field, e.Message)
		}
		return errors.New("invalid options:\n" + strings.TrimRight(b.String(), "\n"))
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
	fmt.Fprintf(out, "%d files (%d already exist and will be kept)\n", len(planned), existing)
	if f.dryRun {
		fmt.Fprintln(out, "dry run: nothing written")
		return nil
	}
	if !f.yes && !confirm(out, in, "Write these files?") {
		fmt.Fprintln(out, "aborted")
		return nil
	}
	res, err := scaffold.Apply(root, files, nil)
	if err != nil {
		return err
	}
	summarise(out, res)
	if f.gitCommit {
		return commitScaffold(out, root, res)
	}
	return nil
}

func runDetect(out io.Writer, in *bufio.Reader, root string, f *initFlags) error {
	rep, err := workspace.Detect(root, nil)
	if err != nil {
		return err
	}
	if !rep.HasIaC() {
		return errors.New("no Terraform or Ansible found here; use --preset to scaffold a new layout")
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
	cfg := config.FromReport(rep)
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
