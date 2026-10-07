package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Location is where a resource is declared (repo-relative).
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// moduleDirs maps module keys ("", "network", "a.b") to directories, using the
// modules.json terraform writes during init.
func moduleDirs(rootDir string) map[string]string {
	out := map[string]string{"": rootDir}
	b, err := os.ReadFile(filepath.Join(rootDir, ".terraform", "modules", "modules.json"))
	if err != nil {
		return out
	}
	var m struct {
		Modules []struct{ Key, Dir string }
	}
	if json.Unmarshal(b, &m) != nil {
		return out
	}
	for _, mod := range m.Modules {
		dir := mod.Dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(rootDir, dir)
		}
		out[mod.Key] = dir
	}
	return out
}

// ModuleDirs returns the local directories a root's configuration is made of
// (the root itself plus local modules), absolute and de-duplicated.
func ModuleDirs(rootDir string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range moduleDirs(rootDir) {
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// Locate finds the declaration of a resource address under rootDir.
func Locate(repoRoot, rootDir, address string) *Location {
	parts := strings.Split(address, ".")
	var mods []string
	i := 0
	for i+1 < len(parts) && parts[i] == "module" {
		name := parts[i+1]
		if j := strings.IndexByte(name, '['); j >= 0 {
			name = name[:j]
		}
		mods = append(mods, name)
		i += 2
	}
	rest := parts[i:]
	blockType := "resource"
	if len(rest) > 0 && rest[0] == "data" {
		blockType, rest = "data", rest[1:]
	}
	if len(rest) < 2 {
		return nil
	}
	typ, name := rest[0], rest[1]
	if j := strings.IndexByte(name, '['); j >= 0 {
		name = name[:j]
	}
	dir, ok := moduleDirs(rootDir)[strings.Join(mods, ".")]
	if !ok {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.tf"))
	sort.Strings(files)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		file, _ := hclsyntax.ParseConfig(src, f, hcl.InitialPos)
		if file == nil {
			continue
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, b := range body.Blocks {
			if b.Type == blockType && len(b.Labels) == 2 && b.Labels[0] == typ && b.Labels[1] == name {
				rel, err := filepath.Rel(repoRoot, f)
				if err != nil {
					return nil
				}
				return &Location{File: filepath.ToSlash(rel), Line: b.TypeRange.Start.Line}
			}
		}
	}
	return nil
}
