package service

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirSuggestion is one folder offered while typing a path to import.
type DirSuggestion struct {
	Path string `json:"path"` // as typed: "~/…" stays "~/…"
	Git  bool   `json:"git"`  // has a .git: importing it imports that repository
}

const maxDirSuggestions = 50

// SuggestDirs lists the folders a partly typed path could continue into:
// "~/go/" lists ~/go's folders, "~/go/sr" those starting with "sr". Only
// "~/…" and absolute paths are completed; hidden folders only when the typed
// name starts with a dot. Names of folders only, never files.
func SuggestDirs(typed string) []DirSuggestion {
	if typed == "~" {
		typed = "~/"
	}
	if typed != "~/" && !strings.HasPrefix(typed, "~/") && !filepath.IsAbs(typed) {
		return nil
	}
	dir, partial := typed[:strings.LastIndex(typed, "/")+1], typed[strings.LastIndex(typed, "/")+1:]
	full, err := expandHome(dir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(partial)
	var out []DirSuggestion
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(partial, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), lower) {
			continue
		}
		p := filepath.Join(full, name)
		if st, err := os.Stat(p); err != nil || !st.IsDir() { // follows symlinks to folders
			continue
		}
		_, gitErr := os.Stat(filepath.Join(p, ".git"))
		out = append(out, DirSuggestion{Path: dir + name, Git: gitErr == nil})
		if len(out) == maxDirSuggestions {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out
}

func (s *Service) suggestDirs(w http.ResponseWriter, r *http.Request) {
	out := SuggestDirs(r.URL.Query().Get("path"))
	if out == nil {
		out = []DirSuggestion{}
	}
	writeJSON(w, 200, out)
}
