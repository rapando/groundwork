package checks

import (
	"path"
	"path/filepath"
	"strings"
)

// repoPath turns a path printed by a tool into a repo-relative, slash-separated
// path. base is the repo-relative directory the tool ran in; root is the
// symlink-resolved repo root (tools often print real paths).
func repoPath(root, base, p string) string {
	if p == "" {
		return base
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		if root != "" {
			if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
				return filepath.ToSlash(r)
			}
		}
		// checkov style: "/main.tf" is relative to the scanned dir
		p = strings.TrimPrefix(p, "/")
	}
	return path.Clean(path.Join(base, filepath.ToSlash(p)))
}
