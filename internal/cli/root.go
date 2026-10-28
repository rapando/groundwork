// Package cli defines the cobra command tree.
package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/rapando/groundwork/v2/internal/checks"
	"github.com/rapando/groundwork/v2/internal/doctor"
	"github.com/rapando/groundwork/v2/internal/server"
	"github.com/rapando/groundwork/v2/internal/service"
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
	logLevel    string
	logFormat   string
}

// envOr is a flag default taken from the environment.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Execute() error { return newRoot().Execute() }

func newRoot() *cobra.Command {
	var f serveFlags
	serve := func(add []string) error {
		return service.Run(context.Background(), service.Options{
			Host: f.host, Port: f.port, NoOpen: f.noOpen, AllowRemote: f.allowRemote,
			Version: Version, Commit: Commit, Add: add,
			// the service writes its own rotated log; a terminal also sees it
			LogLevel: f.logLevel, LogFormat: f.logFormat, LogToStderr: term.IsTerminal(int(os.Stderr.Fd())),
		})
	}
	logFlags := func(c *cobra.Command) {
		c.Flags().StringVar(&f.logLevel, "log-level", envOr("GROUNDWORK_LOG", "info"), "service log level: debug, info, warn or error (env GROUNDWORK_LOG)")
		c.Flags().StringVar(&f.logFormat, "log-format", envOr("GROUNDWORK_LOG_FORMAT", "text"), "service log format: text or json (env GROUNDWORK_LOG_FORMAT)")
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
	root.Flags().IntVar(&f.port, "port", 7420, "port for the service if it has to be started (0 picks a free one)")
	root.Flags().BoolVar(&f.noOpen, "no-open", false, "print the URL instead of opening the browser")
	root.Flags().BoolVar(&f.foreground, "foreground", false, "run the service in this terminal instead of in the background")
	root.Flags().StringVar(&f.host, "host", "127.0.0.1", "address to bind (with --foreground)")
	root.Flags().BoolVar(&f.allowRemote, "i-understand-remote-access", false, "allow binding a non-loopback address (with --foreground)")
	logFlags(root)

	serveCmd := &cobra.Command{
		Use:   "serve [folder...]",
		Short: "Run the service in the foreground (what the login service runs)",
		Long:  "Run the service in the foreground. Folders given are imported as projects, and the browser opens the first.",
		RunE:  func(_ *cobra.Command, args []string) error { return serve(args) },
	}
	serveCmd.Flags().StringVar(&f.host, "host", "127.0.0.1", "address to bind")
	serveCmd.Flags().IntVar(&f.port, "port", 7420, "port to listen on (0 picks a free one; a busy port is an error)")
	serveCmd.Flags().BoolVar(&f.noOpen, "no-open", false, "do not open the browser")
	serveCmd.Flags().BoolVar(&f.allowRemote, "i-understand-remote-access", false, "allow binding a non-loopback address")
	logFlags(serveCmd)

	version := &cobra.Command{
		Use: "version", Short: "Print version information (version, commit, Go version, UI build)",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "groundwork %s (commit %s, %s, ui %s)\n", Version, Commit, runtime.Version(), server.UIHash())
		},
	}
	root.AddCommand(serveCmd, version, newInit(), newCheck(func() checks.Exec { return checks.OSExec{} }), newDoctor(doctor.OSExec{}), newDrift())
	root.AddCommand(newProjectCmds()...)
	root.AddCommand(newServiceCmd(), newAuthCmd())
	return root
}
