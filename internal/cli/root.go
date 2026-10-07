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

	"github.com/rapando/groundwork/internal/app"
	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/doctor"
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
}

func Execute() error { return newRoot().Execute() }

func newRoot() *cobra.Command {
	var f serveFlags
	run := func(cmd *cobra.Command, _ []string) error {
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
		return app.Run(context.Background(), app.Options{
			Host: f.host, Port: f.port, NoOpen: f.noOpen, AllowRemote: f.allowRemote,
			Version: Version, Log: log,
		})
	}
	root := &cobra.Command{
		Use:           "groundwork",
		Short:         "Local web console for Terraform and Ansible",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          run,
	}
	bind := func(c *cobra.Command) {
		c.Flags().StringVar(&f.host, "host", "127.0.0.1", "address to bind")
		c.Flags().IntVar(&f.port, "port", 7420, "preferred port (a free one is used if busy)")
		c.Flags().BoolVar(&f.noOpen, "no-open", false, "do not open the browser")
		c.Flags().BoolVar(&f.allowRemote, "i-understand-remote-access", false, "allow binding a non-loopback address")
	}
	bind(root)

	serve := &cobra.Command{Use: "serve", Short: "Start the web UI", Args: cobra.NoArgs, RunE: run, SilenceUsage: true}
	bind(serve)

	version := &cobra.Command{
		Use: "version", Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "groundwork %s (commit %s)\n", Version, Commit)
		},
	}
	root.AddCommand(serve, version, newInit(), newCheck(func() checks.Exec { return checks.OSExec{} }), newDoctor(doctor.OSExec{}), newDrift())
	return root
}
