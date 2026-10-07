package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

// Rules run over arbitrary (attacker-influenced) tool output: no panics, and
// every hit renders.
func FuzzMatchLog(f *testing.F) {
	files, _ := filepath.Glob("testdata/logs/*.log")
	for _, p := range files {
		b, _ := os.ReadFile(p)
		f.Add(string(b))
	}
	rules, _ := LoadRules("")
	f.Fuzz(func(t *testing.T, text string) {
		for _, scope := range []string{"terraform", "ansible"} {
			for _, h := range MatchLog(rules, scope, text) {
				Render(h.Rule, h, Context{Target: "t", Root: "r", Env: "e"})
			}
		}
	})
}
