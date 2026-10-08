package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/internal/app"
	"github.com/rapando/groundwork/internal/service"
)

// ensureService returns a client for the running service, starting it first
// (through the login service if installed, else in the background).
func ensureService(cmd *cobra.Command, port int) (*service.Client, service.ServerInfo, error) {
	home, err := service.Home()
	if err != nil {
		return nil, service.ServerInfo{}, err
	}
	if si, ok := service.Running(home); ok {
		return service.NewClient(si), si, nil
	}
	if ok, err := service.StartInstalled(); ok && err == nil {
		if si, err := waitRunning(home); err == nil {
			return service.NewClient(si), si, nil
		}
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "starting the groundwork service in the background (`groundwork service install` starts it at login)")
	si, err := service.StartBackground(home, port)
	if err != nil {
		return nil, si, err
	}
	return service.NewClient(si), si, nil
}

func waitRunning(home string) (service.ServerInfo, error) {
	for range 80 {
		if si, ok := service.Running(home); ok {
			return si, nil
		}
		sleep()
	}
	return service.ServerInfo{}, fmt.Errorf("groundwork service did not start; see %s", service.LogPath(home))
}

func openURL(cmd *cobra.Command, u string, noOpen bool) error {
	fmt.Fprintln(cmd.OutOrStdout(), u)
	if !noOpen {
		_ = browser.OpenURL(u)
	}
	return nil
}

// isRemote tells a repository URL from a local folder.
func isRemote(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "ssh://") || (strings.Contains(s, "@") && strings.Contains(s, ":") && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "."))
}

// findProject matches an id, then a name, then an existing folder (or one
// inside a project). A ref that is none of these never falls back to an
// enclosing repository: `remove typo` must not remove the project you're in.
func findProject(list []service.ProjectView, ref string) (service.ProjectView, error) {
	var byName []service.ProjectView
	for _, p := range list {
		if p.ID == ref {
			return p, nil
		}
		if p.Name == ref {
			byName = append(byName, p)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
	default:
		return service.ProjectView{}, fmt.Errorf("%d projects are named %q; use the id", len(byName), ref)
	}
	if fi, err := os.Stat(ref); err == nil && fi.IsDir() {
		root, err := app.FindRoot(ref)
		if err == nil {
			if r, err := filepath.EvalSymlinks(root); err == nil {
				root = r
			}
			for _, p := range list {
				if p.Path == root {
					return p, nil
				}
			}
		}
	}
	return service.ProjectView{}, fmt.Errorf("no project matches %q (see `groundwork projects`)", ref)
}

func newProjectCmds() []*cobra.Command {
	var noOpen bool
	add := &cobra.Command{
		Use:   "add <folder | git URL>...",
		Short: "Import repositories as projects (a URL is cloned)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, si, err := ensureService(cmd, 7420)
			if err != nil {
				return err
			}
			for _, a := range args {
				var p service.Project
				if isRemote(a) {
					fmt.Fprintln(cmd.ErrOrStderr(), "cloning", a, "…")
					p, err = c.AddURL(a)
				} else {
					p, err = c.AddPath(a)
				}
				if err != nil {
					return fmt.Errorf("%s: %w", a, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", p.ID, p.Path, si.URL(service.ProjectPath(p.ID)))
			}
			return nil
		},
	}

	list := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"ls"},
		Short:   "List imported projects",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := ensureService(cmd, 7420)
			if err != nil {
				return err
			}
			ps, err := c.List()
			if err != nil {
				return err
			}
			if len(ps) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no projects yet: run `groundwork` in a repository, or `groundwork add <folder | git URL>`")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tPATH")
			for _, p := range ps {
				st := p.Status
				switch {
				case p.Summary == nil:
				case p.ActiveRuns > 0:
					st = fmt.Sprintf("%d running", p.ActiveRuns)
				case !p.Configured:
					st = "needs setup"
				case p.OpenIssues > 0:
					st = fmt.Sprintf("%d issues", p.OpenIssues)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", p.ID, st, p.Path)
			}
			return tw.Flush()
		},
	}

	remove := &cobra.Command{
		Use:     "remove <id | name | folder>",
		Aliases: []string{"rm"},
		Short:   "Stop managing a project (its files are left in place)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := ensureService(cmd, 7420)
			if err != nil {
				return err
			}
			ps, err := c.List()
			if err != nil {
				return err
			}
			p, err := findProject(ps, args[0])
			if err != nil {
				return err
			}
			if err := c.Remove(p.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s (files left in %s)\n", p.ID, p.Path)
			return nil
		},
	}

	open := &cobra.Command{
		Use:   "open [id | name | folder]",
		Short: "Open the console in the browser (the project list, or one project)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, si, err := ensureService(cmd, 7420)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return openURL(cmd, si.URL("/"), noOpen)
			}
			ps, err := c.List()
			if err != nil {
				return err
			}
			p, err := findProject(ps, args[0])
			if err != nil {
				return err
			}
			return openURL(cmd, si.URL(service.ProjectPath(p.ID)), noOpen)
		},
	}
	open.Flags().BoolVar(&noOpen, "no-open", false, "print the URL only")
	return []*cobra.Command{add, list, remove, open}
}
