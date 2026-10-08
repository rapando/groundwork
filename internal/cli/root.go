// Package cli defines the cobra command tree.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/doctor"
	"github.com/rapando/groundwork/internal/service"
)

// Set via -ldflags at release time.
var (
	Version = "dev"
	Commit  = "none"
)

// `go install ...@vX.Y.Z` builds without ldflags; take the version from the module instead.
func init() {
	if Version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		Version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

type serveFlags struct {
	host        string
	port        int
	noOpen      bool
	allowRemote bool
	foreground  bool
}

func Execute() error { return newRoot().Execute() }

func newRoot() *cobra.Command {
	var f serveFlags
	logger := func() *slog.Logger {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	serve := func(add []string) error {
		return service.Run(context.Background(), service.Options{
			Host: f.host, Port: f.port, NoOpen: f.noOpen, AllowRemote: f.allowRemote,
			Version: Version, Log: logger(), Add: add,
		})
	}
	root := &cobra.Command{
		Use:   "groundwork",
		Short: "Local web console for Terraform and Ansible",
		Long: "groundwork runs as a background service that hosts all your infrastructure projects.\n" +
			"Run it in a repository to import that repository and open it in the browser;\n" +
			"the service is started if it isn't running.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.foreground {
				return serve([]string{"."})
			}
			c, si, err := ensureService(cmd, f.port)
			if err != nil {
				return err
			}
			p, err := c.AddPath(".")
			if err != nil {
				return err
			}
			return openURL(cmd, si.URL(service.ProjectPath(p.ID)), f.noOpen)
		},
	}
	root.Flags().IntVar(&f.port, "port", 7420, "port for the service if it has to be started (a free one is used if busy)")
	root.Flags().BoolVar(&f.noOpen, "no-open", false, "print the URL instead of opening the browser")
	root.Flags().BoolVar(&f.foreground, "foreground", false, "run the service in this terminal instead of in the background")
	root.Flags().StringVar(&f.host, "host", "127.0.0.1", "address to bind (with --foreground)")
	root.Flags().BoolVar(&f.allowRemote, "i-understand-remote-access", false, "allow binding a non-loopback address (with --foreground)")

	serveCmd := &cobra.Command{
		Use:   "serve [folder...]",
		Short: "Run the service in the foreground (what the login service runs)",
		Long:  "Run the service in the foreground. Folders given are imported as projects, and the browser opens the first.",
		RunE:  func(_ *cobra.Command, args []string) error { return serve(args) },
	}
	serveCmd.Flags().StringVar(&f.host, "host", "127.0.0.1", "address to bind")
	serveCmd.Flags().IntVar(&f.port, "port", 7420, "preferred port (a free one is used if busy)")
	serveCmd.Flags().BoolVar(&f.noOpen, "no-open", false, "do not open the browser")
	serveCmd.Flags().BoolVar(&f.allowRemote, "i-understand-remote-access", false, "allow binding a non-loopback address")

	version := &cobra.Command{
		Use: "version", Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "groundwork %s (commit %s)\n", Version, Commit)
		},
	}
	root.AddCommand(serveCmd, version, newInit(), newCheck(func() checks.Exec { return checks.OSExec{} }), newDoctor(doctor.OSExec{}), newDrift())
	root.AddCommand(newProjectCmds()...)
	root.AddCommand(newServiceCmd())
	return root
}
