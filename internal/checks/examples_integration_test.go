//go:build integration

package checks

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/workspace"
)

// Every example must pass every installed tool with zero findings.
func TestExamplesAreCleanUnderRealTools(t *testing.T) {
	for _, ex := range []string{"terraform-only", "ansible-only", "app-with-infra"} {
		t.Run(ex, func(t *testing.T) {
			dir := t.TempDir() // copy: checks must not write into the examples
			src, _ := filepath.Abs("../../examples/" + ex)
			filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
				rel, _ := filepath.Rel(src, p)
				if d.IsDir() {
					return os.MkdirAll(filepath.Join(dir, rel), 0o755)
				}
				b, _ := os.ReadFile(p)
				info, _ := d.Info()
				return os.WriteFile(filepath.Join(dir, rel), b, info.Mode().Perm())
			})
			rep, _ := workspace.Detect(dir, nil)
			cfg := config.FromReport(rep)
			yml, _ := cfg.Marshal() // checked too: groundwork's own file must lint clean
			os.WriteFile(filepath.Join(dir, "groundwork.yaml"), yml, 0o644)

			st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
			defer st.Close()
			svc := New(dir, func() *config.Config { return cfg }, OSExec{}, st, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			svc.Run(context.Background(), "", true)
			ran := 0
			for _, u := range svc.Status() {
				for _, r := range u.Results {
					if r.Status == "failed" {
						t.Errorf("%s %s failed: %s", u.ID, r.Tool, r.Message)
					}
					if r.Status != "skipped" {
						ran++
					}
				}
			}
			ds, _ := svc.Diagnostics("", "", "")
			for _, d := range ds {
				t.Errorf("%s:%d %s [%s %s] %s", d.File, d.Line, d.Severity, d.Tool, d.Code, d.Message)
			}
			if ran == 0 {
				t.Skip("no tools installed")
			}
		})
	}
}
