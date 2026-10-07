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

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/internal/app"
	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

// ExitError carries a process exit code out of a command (and an optional message).
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// Exit codes for `groundwork check`.
const (
	exitFindings   = 1 // diagnostics at or above --fail-on
	exitToolFailed = 3 // a tool could not run, so the result is incomplete
)

type checkFlags struct {
	json   bool
	unit   string
	failOn string
}

// newCheck builds `groundwork check`. mkExec is injectable for tests.
func newCheck(mkExec func() checks.Exec) *cobra.Command {
	var f checkFlags
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run all checks once (CI-friendly)",
		Long: `Runs the same pipeline as the UI (fmt, validate, tflint, ansible-lint, yamllint,
syntax-check, ...) once and exits non-zero on problems.

Exit codes: 0 clean, 1 findings at or above --fail-on, 3 a tool failed to run
(for example terraform init could not reach the registry) so the result is
incomplete. Tools that are not installed are reported as skipped, not failed.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd.Context(), cmd.OutOrStdout(), mkExec(), &f)
		},
	}
	cmd.Flags().BoolVar(&f.json, "json", false, "machine-readable output")
	cmd.Flags().StringVar(&f.unit, "unit", "", "check one unit only, e.g. tf:terraform/envs/dev")
	cmd.Flags().StringVar(&f.failOn, "fail-on", "error", "lowest severity that fails the run: error|warning")
	return cmd
}

func runCheck(ctx context.Context, out io.Writer, ex checks.Exec, f *checkFlags) error {
	if f.failOn != "error" && f.failOn != "warning" {
		return &ExitError{2, "--fail-on must be error or warning"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := app.FindRoot(".")
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if errors.Is(err, os.ErrNotExist) {
		return &ExitError{2, "no " + config.FileName + " in " + root + "; run `groundwork init` first"}
	}
	if err != nil {
		return &ExitError{2, err.Error()}
	}
	stateDir, err := app.Bootstrap(root)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := checks.New(root, func() *config.Config { return cfg }, ex, st, events.NewBus(), log)
	if f.unit != "" {
		found := false
		for _, u := range svc.Units() {
			found = found || u.ID == f.unit
		}
		if !found {
			return &ExitError{2, "unknown unit " + f.unit}
		}
	}
	if len(svc.Units()) == 0 {
		return &ExitError{2, "nothing to check: no units are configured (see checks.enabled in " + config.FileName + ")"}
	}
	if err := svc.Run(ctx, f.unit, true); err != nil {
		return err
	}

	diags, err := svc.Diagnostics("", "", "")
	if err != nil {
		return err
	}
	status := svc.Status()
	if f.unit != "" {
		var keep []checks.Diagnostic
		for _, d := range diags {
			if d.Unit == f.unit {
				keep = append(keep, d)
			}
		}
		diags = keep
		var one []checks.UnitStatus
		for _, u := range status {
			if u.ID == f.unit {
				one = append(one, u)
			}
		}
		status = one
	}

	counts := map[string]int{"error": 0, "warning": 0, "info": 0}
	for _, d := range diags {
		counts[d.Severity]++
	}
	failed := false
	for _, u := range status {
		for _, r := range u.Results {
			failed = failed || r.Status == "failed"
		}
	}

	if f.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if diags == nil {
			diags = []checks.Diagnostic{}
		}
		if err := enc.Encode(map[string]any{"diagnostics": diags, "units": status, "counts": counts}); err != nil {
			return err
		}
	} else {
		printCheck(out, diags, status, counts)
	}

	switch {
	case counts["error"] > 0 || (f.failOn == "warning" && counts["warning"] > 0):
		return &ExitError{exitFindings, ""}
	case failed:
		return &ExitError{exitToolFailed, ""}
	}
	return nil
}

func printCheck(out io.Writer, diags []checks.Diagnostic, status []checks.UnitStatus, counts map[string]int) {
	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].File != diags[j].File {
			return diags[i].File < diags[j].File
		}
		return diags[i].Line < diags[j].Line
	})
	for _, d := range diags {
		loc := d.File
		if d.Line > 0 {
			loc = fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Col)
		}
		code := ""
		if d.Code != "" {
			code = " " + d.Code
		}
		fmt.Fprintf(out, "%s: %s [%s%s] %s\n", loc, d.Severity, d.Tool, code, d.Message)
		if d.Detail != "" && d.Tool == "validate" {
			fmt.Fprintf(out, "    %s\n", strings.ReplaceAll(strings.TrimSpace(d.Detail), "\n", "\n    "))
		}
	}
	if len(diags) > 0 {
		fmt.Fprintln(out)
	}
	for _, u := range status {
		var parts []string
		for _, r := range u.Results {
			s := r.Tool + " " + map[string]string{"ok": "ok", "issues": fmt.Sprintf("%d", r.Count), "skipped": "skipped", "failed": "FAILED", "idle": "not run"}[r.Status]
			parts = append(parts, s)
		}
		fmt.Fprintf(out, "%-40s %s\n", u.Path, strings.Join(parts, " · "))
	}
	for _, u := range status {
		for _, r := range u.Results {
			switch r.Status {
			case "skipped":
				fmt.Fprintf(out, "note: %s skipped for %s: %s", r.Tool, u.Path, r.Message)
				if r.Hint != "" {
					fmt.Fprintf(out, " (%s)", r.Hint)
				}
				fmt.Fprintln(out)
			case "failed":
				fmt.Fprintf(out, "error: %s could not run for %s: %s\n", r.Tool, u.Path, r.Message)
			}
		}
	}
	fmt.Fprintf(out, "\n%d errors, %d warnings, %d info\n", counts["error"], counts["warning"], counts["info"])
}
