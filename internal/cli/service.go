package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/v2/internal/service"
)

func sleep() { time.Sleep(250 * time.Millisecond) }

// serviceStatus asks a running service what it is; ok is false when it doesn't answer.
func serviceStatus(si service.ServerInfo) (service.Status, bool) {
	st, err := service.NewClient(si).Status()
	return st, err == nil
}

// version names a release with its commit, e.g. "2.0.0 (commit 3c3e367)".
func version(v, commit string) string {
	if v == "" {
		v = "unknown version"
	}
	if commit == "" || commit == "none" {
		return v
	}
	return fmt.Sprintf("%s (commit %s)", v, commit)
}

// uptime rounds to seconds under a minute, else to minutes.
func uptime(sec int64) string {
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return d.String()
	}
	return strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
}

// printRunning says which groundwork is serving and where, after a start,
// restart or install. what is e.g. "started" or "already running".
func printRunning(out io.Writer, what string, si service.ServerInfo) {
	st, ok := serviceStatus(si)
	if !ok {
		fmt.Fprintf(out, "groundwork %s %s (pid %d)\n", si.Version, what, si.PID)
		fmt.Fprintln(out, "  url:      ", si.URL("/"))
		return
	}
	fmt.Fprintf(out, "groundwork %s %s (pid %d", version(st.Version, st.Commit), what, st.PID)
	if st.UptimeSec >= 60 {
		fmt.Fprintf(out, ", up %s", uptime(st.UptimeSec))
	}
	fmt.Fprintln(out, ")")
	fmt.Fprintln(out, "  url:      ", si.URL("/"))
	projects := fmt.Sprintf("%d available", st.Projects.Available)
	if st.Projects.Unavailable > 0 {
		projects += fmt.Sprintf(", %d unavailable", st.Projects.Unavailable)
	}
	fmt.Fprintln(out, "  projects: ", projects)
	if st.RunningJobs > 0 {
		fmt.Fprintf(out, "  running:   %d runs in progress\n", st.RunningJobs)
	}
	if st.LogFile != "" {
		fmt.Fprintln(out, "  log:      ", st.LogFile)
	}
	if Version != st.Version && Version != "dev" && st.Version != "dev" {
		fmt.Fprintf(out, "  note:      this command is %s\n", Version)
	}
}

// stopService stops the service and says what was stopped. A service that
// isn't running is an error unless allowNotRunning.
func stopService(out io.Writer, home string, allowNotRunning bool) (service.Status, error) {
	si, running := service.Running(home)
	var st service.Status
	if running {
		st, _ = serviceStatus(si)
	}
	err := service.Stop(home)
	switch {
	case err != nil && allowNotRunning && strings.Contains(err.Error(), "not running"):
		fmt.Fprintln(out, "groundwork service was not running")
		return st, nil
	case err != nil:
		return st, err
	case !running:
		fmt.Fprintln(out, "stopped the login service")
		return st, nil
	}
	v := version(st.Version, st.Commit)
	if st.Version == "" {
		v = si.Version
	}
	fmt.Fprintf(out, "groundwork %s stopped (pid %d", v, si.PID)
	if st.UptimeSec > 0 {
		fmt.Fprintf(out, ", was up %s", uptime(st.UptimeSec))
	}
	fmt.Fprintln(out, ")")
	if st.RunningJobs > 0 {
		fmt.Fprintf(out, "  %d runs in progress were stopped gracefully\n", st.RunningJobs)
	}
	return st, nil
}

func newServiceCmd() *cobra.Command {
	svc := &cobra.Command{
		Use:   "service",
		Short: "Manage the background service (install starts it at login)",
	}
	var port int
	install := &cobra.Command{
		Use:   "install",
		Short: "Start groundwork at login (launchd on macOS, systemd --user on Linux) and start it now",
		Long: "Start groundwork at login and start it now. The service runs with the PATH of the shell\n" +
			"you install it from, so it finds the same terraform, ansible and linters; re-run install\n" +
			"after changing your toolchain's location.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			path, err := service.Install(home)
			if err != nil {
				return err
			}
			si, err := waitRunning(home)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "installed the login service:", path)
			printRunning(cmd.OutOrStdout(), "running", si)
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: "Stop the service and remove it from login items", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			if si, ok := service.Running(home); ok {
				if st, ok := serviceStatus(si); ok {
					fmt.Fprintf(cmd.OutOrStdout(), "stopping groundwork %s (pid %d)\n", version(st.Version, st.Commit), st.PID)
				}
			}
			path, err := service.Uninstall()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed the login service:", path)
			return nil
		},
	}
	start := &cobra.Command{
		Use: "start", Short: "Start the service", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			_, wasRunning := service.Running(home)
			_, si, err := ensureService(cmd, port)
			if err != nil {
				return err
			}
			what := "started"
			if wasRunning {
				what = "already running"
			}
			printRunning(cmd.OutOrStdout(), what, si)
			return nil
		},
	}
	start.Flags().IntVar(&port, "port", 7420, "port when started in the background")
	restart := &cobra.Command{
		Use: "restart", Short: "Stop the service and start it again (after installing a new version)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			old, err := stopService(out, home, true)
			if err != nil {
				return err
			}
			_, si, err := ensureService(cmd, port)
			if err != nil {
				return err
			}
			printRunning(out, "started", si)
			if old.Version != "" && si.Version != "" && old.Version != si.Version {
				fmt.Fprintf(out, "upgraded from %s to %s\n", old.Version, si.Version)
			}
			return nil
		},
	}
	restart.Flags().IntVar(&port, "port", 7420, "port when started in the background")
	stop := &cobra.Command{
		Use: "stop", Short: "Stop the service (runs in progress are stopped gracefully)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			_, err = stopService(cmd.OutOrStdout(), home, false)
			return err
		},
	}
	status := &cobra.Command{
		Use: "status", Short: "Show whether the service is running, and what it is doing", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			login := "not installed"
			if service.Installed() {
				login = "installed"
			}
			fmt.Fprintln(out, "data:         ", home)
			fmt.Fprintln(out, "log:          ", service.LogPath(home))
			fmt.Fprintln(out, "login service:", login)
			si, ok := service.Running(home)
			if !ok {
				fmt.Fprintln(out, "status:        stopped")
				return nil
			}
			st, err := service.NewClient(si).Status()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "status:        running (pid %d), up %s\n", st.PID, (time.Duration(st.UptimeSec) * time.Second).String())
			fmt.Fprintf(out, "version:       %s (commit %s, %s; this command: %s)\n", st.Version, st.Commit, st.GoVersion, Version)
			if m := versionMismatch(si, Version); m != "" {
				fmt.Fprintln(out, "warning:      ", m)
			}
			fmt.Fprintf(out, "port:          %d\n", st.Port)
			fmt.Fprintf(out, "projects:      %d available, %d unavailable\n", st.Projects.Available, st.Projects.Unavailable)
			fmt.Fprintf(out, "running jobs:  %d\n", st.RunningJobs)
			fmt.Fprintln(out, "url:          ", si.URL("/"))
			return nil
		},
	}
	var follow bool
	var lines int
	logs := &cobra.Command{
		Use: "logs", Short: "Print the service log (rotated at 10 MiB, three old files kept)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return service.TailLog(ctx, cmd.OutOrStdout(), home, lines, follow)
		},
	}
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines until interrupted")
	logs.Flags().IntVarP(&lines, "lines", "n", 100, "how many lines from the end to print")
	svc.AddCommand(install, uninstall, start, restart, stop, status, logs)
	return svc
}
