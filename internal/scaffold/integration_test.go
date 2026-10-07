//go:build integration

package scaffold

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/checks"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

// Scaffolds every variant and runs the real tools over the result. A fresh
// scaffold must be completely clean: a starter that ships warnings teaches
// people to ignore the checks. Tools that are not installed are skipped.
func TestScaffoldIsCleanUnderRealTools(t *testing.T) {
	network := os.Getenv("GW_TEST_NETWORK") == "1" // aws variants download the aws provider
	for name, f := range variants() {
		if f.Preset == "aws-envs" && !network {
			continue
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fs, err := Render(&f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(dir, fs, nil); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			svc := checks.New(dir, func() *config.Config { return cfg }, checks.OSExec{}, st, events.NewBus(),
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := svc.Run(context.Background(), "", true); err != nil {
				t.Fatal(err)
			}
			ran := map[string]bool{}
			for _, u := range svc.Status() {
				for _, r := range u.Results {
					switch r.Status {
					case "failed":
						t.Errorf("%s %s failed: %s", u.ID, r.Tool, r.Message)
					case "skipped":
						t.Logf("skipped %s (%s)", r.Tool, r.Message)
					default:
						ran[r.Tool] = true
					}
				}
			}
			ds, _ := svc.Diagnostics("", "", "")
			for _, d := range ds {
				t.Errorf("%s:%d:%d %s [%s %s] %s", d.File, d.Line, d.Col, d.Severity, d.Tool, d.Code, d.Message)
			}
			t.Logf("tools that actually ran: %v", keys(ran))
			if len(ran) == 0 {
				t.Skip("no check tools installed")
			}
		})
	}
	_ = strings.Join
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
