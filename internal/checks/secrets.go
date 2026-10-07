package checks

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rapando/groundwork/internal/vars"
)

// scanSecrets is the built-in "secrets" check: plaintext credentials in the
// unit's files. A Terraform unit covers only its own directory (modules are
// units of their own); an Ansible project is walked recursively.
func (s *Service) scanSecrets(ctx context.Context, u Unit) []Diagnostic {
	dir := s.abs(u.Path)
	var ds []Diagnostic
	visit := func(p string) {
		if !vars.Scannable(p) {
			return
		}
		info, err := os.Stat(p)
		if err != nil || info.Size() > 1<<20 {
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		rel, _ := filepath.Rel(s.Root, p)
		for _, f := range vars.Scan(filepath.ToSlash(rel), b) {
			ds = append(ds, Diagnostic{Tool: "secrets", Severity: f.Severity, Code: f.Rule, Message: f.Message,
				Detail: "Move it to ansible-vault, SOPS or an environment variable, then rotate it: it may already be in git history.",
				File:   f.File, Line: f.Line, Col: f.Col})
		}
	}
	if u.Kind == KindAnsible {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil {
				return filepath.SkipDir
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", ".groundwork", "node_modules", ".venv", "venv", "collections", ".terraform":
					return filepath.SkipDir
				}
				return nil
			}
			visit(p)
			return nil
		})
		return ds
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			visit(filepath.Join(dir, e.Name()))
		}
	}
	return ds
}
