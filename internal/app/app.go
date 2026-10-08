// Package app holds what every command needs to locate a repository and its
// .groundwork/ state directory.
package app

import (
	"errors"
	"os"
	"path/filepath"
)

const StateDir = ".groundwork"

// FindRoot returns the first ancestor of dir containing .git, or dir itself.
func FindRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs, nil
		}
		d = parent
	}
}

// Bootstrap creates .groundwork/ with a self-ignoring .gitignore so the repo's
// own files are never touched.
func Bootstrap(root string) (string, error) {
	dir := filepath.Join(root, StateDir)
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return "", err
	}
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(gi, []byte("*\n"), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}
