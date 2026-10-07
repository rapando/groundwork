package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

const MaxFiles = 50000

var hardSkip = map[string]bool{
	".git": true, ".terraform": true, "node_modules": true, "vendor": true,
	".venv": true, ".groundwork": true,
}

// walkResult is the filtered file inventory of a repo.
type walkResult struct {
	files     []string            // repo-relative, slash separated, sorted
	dirs      []string            // repo-relative directories that were entered
	byDir     map[string][]string // dir ("." for root) → base names
	truncated bool
}

func walk(root string, ignore []string) (*walkResult, error) {
	var pats []gitignore.Pattern
	addPatterns := func(lines []string) {
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			pats = append(pats, gitignore.ParsePattern(l, nil))
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, ".gitignore")); err == nil {
		addPatterns(strings.Split(string(b), "\n"))
	}
	addPatterns(ignore)
	matcher := gitignore.NewMatcher(pats)

	res := &walkResult{byDir: map[string][]string{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if hardSkip[d.Name()] || matcher.Match(strings.Split(rel, "/"), true) {
				return filepath.SkipDir
			}
			res.dirs = append(res.dirs, rel)
			return nil
		}
		if !d.Type().IsRegular() || matcher.Match(strings.Split(rel, "/"), false) {
			return nil
		}
		if len(res.files) >= MaxFiles {
			res.truncated = true
			return filepath.SkipAll
		}
		res.files = append(res.files, rel)
		dir := "."
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			dir = rel[:i]
		}
		res.byDir[dir] = append(res.byDir[dir], d.Name())
		return nil
	})
	sort.Strings(res.files)
	sort.Strings(res.dirs)
	return res, err
}

// Tree is the filtered inventory of a repository: what groundwork shows and watches.
type Tree struct {
	Files     []string // repo-relative, slash separated, sorted
	Dirs      []string // directories that were entered ("." excluded)
	Truncated bool     // stopped at MaxFiles
}

// ListTree walks root honouring .gitignore, ignore patterns and the built-in skips.
func ListTree(root string, ignore []string) (*Tree, error) {
	w, err := walk(root, ignore)
	if err != nil {
		return nil, err
	}
	return &Tree{Files: w.files, Dirs: w.dirs, Truncated: w.truncated}, nil
}
