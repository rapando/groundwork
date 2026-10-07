package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/internal/app"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/doctor"
	"github.com/rapando/groundwork/internal/drift"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/workspace"
)

// openRepo loads the config (if any) and the state database.
func openRepo(needConfig bool) (root string, cfg *config.Config, st *store.Store, err error) {
	root, err = app.FindRoot(".")
	if err != nil {
		return
	}
	cfg, err = config.Load(root)
	if errors.Is(err, os.ErrNotExist) {
		if needConfig {
			err = &ExitError{2, "no " + config.FileName + " in " + root + "; run `groundwork init` first"}
			return
		}
		cfg, err = nil, nil
	}
	if err != nil {
		err = &ExitError{2, err.Error()}
		return
	}
	stateDir, err := app.Bootstrap(root)
	if err != nil {
		return
	}
	st, err = store.Open(filepath.Join(stateDir, "state.db"))
	return
}

func newDoctor(ex doctor.Exec) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "doctor",
		Short:        "Check tools, credentials, backends and connectivity",
		Long:         "Runs the same environment checks as the Troubleshoot screen and prints a table.\nExit code 1 if any check fails.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, cfg, st, err := openRepo(false)
			if err != nil {
				return err
			}
			defer st.Close()
			var providers []string
			var ignore []string
			if cfg != nil {
				ignore = cfg.Ignore
			}
			if rep, err := workspace.Detect(root, ignore); err == nil {
				seen := map[string]bool{}
				for _, d := range append(rep.TerraformRoots, rep.TerraformModules...) {
					for _, p := range d.Providers {
						if !seen[p] {
							seen[p] = true
							providers = append(providers, p)
						}
					}
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			rep := doctor.Run(ctx, doctor.Input{Root: root, Cfg: cfg, Providers: providers, Store: st, Exec: ex})
			if b, err := json.Marshal(rep); err == nil {
				_ = st.SetKV("doctor.last", string(b)) // the UI shows the latest run
			}
			return printDoctor(cmd.OutOrStdout(), rep, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func printDoctor(out io.Writer, rep doctor.Report, asJSON bool) error {
	failed := false
	for _, c := range rep.Checks {
		failed = failed || c.Status == "fail"
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, c := range rep.Checks {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", strings.ToUpper(c.Status), c.Name, c.Detail)
			if c.Hint != "" && c.Status != "ok" {
				fmt.Fprintf(tw, "\t\t→ %s\n", c.Hint)
			}
		}
		tw.Flush()
	}
	if failed {
		return &ExitError{1, ""}
	}
	return nil
}

func newDrift() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:          "drift",
		Short:        "Check every Terraform environment for drift (refresh-only, changes nothing)",
		Long:         "Runs a refresh-only plan per Terraform root and environment and lists resources\nthat changed outside Terraform. Exit code 1 if anything drifted, 3 if a check failed.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, cfg, st, err := openRepo(true)
			if err != nil {
				return err
			}
			defer st.Close()
			if env != "" {
				var keep []config.TFRoot
				for _, r := range cfg.Terraform.Roots {
					if r.Env == env {
						keep = append(keep, r)
					} else if e, ok := r.Envs[env]; ok {
						r.Envs = map[string]config.TFEnv{env: e}
						keep = append(keep, r)
					}
				}
				if len(keep) == 0 {
					return &ExitError{2, "no Terraform root has an environment named " + env}
				}
				c := *cfg
				c.Terraform.Roots = keep
				cfg = &c
			}
			cur := func() *config.Config { return cfg }
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
			rn := runner.New(root, st, events.NewBus(), cur, log)
			defer rn.Shutdown(context.Background())
			s := drift.New(rn, st, cur, log)
			defer s.Stop()
			s.Notify = func(context.Context, config.Drift, drift.Notice) error { return nil } // the terminal is the notification
			s.RunAll(cmd.Context())

			runs, _ := st.ListRuns(store.RunFilter{Kind: runner.KindDrift, Limit: 200})
			out := cmd.OutOrStdout()
			drifted, failed := false, false
			type key struct{ root, env string }
			reported := map[key]bool{}
			var lines []string
			for _, r := range runs { // newest first: report each target's latest check
				var t runner.Target
				_ = json.Unmarshal(r.Target, &t)
				k := key{t.Root, t.Env}
				if reported[k] || !inScope(cfg, t) {
					continue
				}
				reported[k] = true
				if r.Status != store.StatusSucceeded {
					failed = true
					lines = append(lines, fmt.Sprintf("FAILED  %s (%s): see run #%d", t.Root, t.Env, r.ID))
					continue
				}
				rows, _ := st.ListDrift(t.Root, t.Env)
				addrs := map[string]bool{}
				for _, d := range rows {
					addrs[d.Address] = true
				}
				if len(addrs) == 0 {
					lines = append(lines, fmt.Sprintf("OK      %s (%s): no drift", t.Root, t.Env))
					continue
				}
				drifted = true
				var list []string
				for a := range addrs {
					list = append(list, a)
				}
				sort.Strings(list)
				lines = append(lines, fmt.Sprintf("DRIFT   %s (%s): %s", t.Root, t.Env, strings.Join(list, ", ")))
			}
			sort.Strings(lines)
			for _, l := range lines {
				fmt.Fprintln(out, l)
			}
			switch {
			case failed:
				return &ExitError{3, ""}
			case drifted:
				return &ExitError{1, ""}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "only this environment")
	return cmd
}

func inScope(cfg *config.Config, t runner.Target) bool {
	for _, r := range cfg.Terraform.Roots {
		if r.Path == t.Root && (r.Env == t.Env || hasEnv(r, t.Env)) {
			return true
		}
	}
	return false
}

func hasEnv(r config.TFRoot, env string) bool { _, ok := r.Envs[env]; return ok }
