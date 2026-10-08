package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/internal/service"
)

func sleep() { time.Sleep(250 * time.Millisecond) }

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
			fmt.Fprintln(cmd.OutOrStdout(), "installed", path)
			fmt.Fprintln(cmd.OutOrStdout(), si.URL("/"))
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: "Stop the service and remove it from login items", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := service.Uninstall()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed", path)
			return nil
		},
	}
	start := &cobra.Command{
		Use: "start", Short: "Start the service", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, si, err := ensureService(cmd, port)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "running:", si.URL("/"))
			return nil
		},
	}
	start.Flags().IntVar(&port, "port", 7420, "port when started in the background")
	stop := &cobra.Command{
		Use: "stop", Short: "Stop the service (runs in progress are stopped gracefully)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			if err := service.Stop(home); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "stopped")
			return nil
		},
	}
	status := &cobra.Command{
		Use: "status", Short: "Show whether the service is running", Args: cobra.NoArgs,
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
			ps, err := service.NewClient(si).List()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "status:        running (pid %d), %d projects\n", si.PID, len(ps))
			fmt.Fprintln(out, "url:          ", si.URL("/"))
			return nil
		},
	}
	svc.AddCommand(install, uninstall, start, stop, status)
	return svc
}
