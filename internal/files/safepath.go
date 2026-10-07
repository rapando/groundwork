// Package files provides repo-rooted filesystem access.
package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrOutsideRepo = errors.New("path escapes repository")

// SafePath cleans rel and returns the absolute path under root. It rejects
// absolute paths, ".." escapes and symlinks that resolve outside root.
// The target need not exist; its deepest existing ancestor is checked.
func SafePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: empty path", ErrOutsideRepo)
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: NUL in path", ErrOutsideRepo)
	}
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, string(filepath.Separator)) {
		return "", fmt.Errorf("%w: absolute path", ErrOutsideRepo)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrOutsideRepo, rel)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(realRoot, clean)

	// Resolve the deepest existing ancestor and make sure it is inside root.
	probe := full
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if !within(realRoot, resolved) {
				return "", fmt.Errorf("%w: %q resolves outside", ErrOutsideRepo, rel)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	return full, nil
}

func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
