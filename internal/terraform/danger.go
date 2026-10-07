package terraform

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Danger flags a destructive change that needs an explicit acknowledgement.
type Danger struct {
	Address  string `json:"address"`
	Action   string `json:"action"` // delete | replace
	Category string `json:"category"`
	Message  string `json:"message"`
}

// DefaultDangerPatterns: resource types whose destruction loses data.
var DefaultDangerPatterns = map[string][]string{
	"database": {"*_db_*", "*_rds_*", "*_database*"},
	"bucket":   {"*_bucket*"},
	"volume":   {"*_volume*"},
	"disk":     {"*_disk*"},
}

var consequence = map[string]string{
	"database":  "Data in the current database is lost unless you take a snapshot first.",
	"bucket":    "Objects stored in the bucket are deleted with it.",
	"volume":    "Data on the volume is lost unless you snapshot it first.",
	"disk":      "Data on the disk is lost unless you snapshot it first.",
	"protected": "This resource has prevent_destroy set in another environment.",
	"custom":    "This resource type is marked dangerous in groundwork.yaml.",
}

func matchAny(patterns []string, typ string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, typ); ok {
			return true
		}
	}
	return false
}

// typeAndName strips module and index parts: module.a.aws_db_instance.main[0] → aws_db_instance.main
func typeAndName(addr string) string {
	parts := strings.Split(addr, ".")
	var keep []string
	for i := 0; i < len(parts); i++ {
		if strings.HasPrefix(parts[i], "module") && i+1 < len(parts) {
			i++
			continue
		}
		keep = append(keep, parts[i])
	}
	s := strings.Join(keep, ".")
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}
	return s
}

// EvaluateDanger returns the destructive changes that need acknowledgement.
// extra are configured type patterns; protected holds "type.name" keys that
// have prevent_destroy somewhere in the repo.
func EvaluateDanger(s *PlanSummary, extra []string, protected map[string]bool) []Danger {
	out := []Danger{}
	for _, c := range s.Changes {
		if c.Action != "delete" && c.Action != "replace" {
			continue
		}
		cat := ""
		for _, k := range []string{"database", "bucket", "volume", "disk"} {
			if matchAny(DefaultDangerPatterns[k], c.Type) {
				cat = k
				break
			}
		}
		if cat == "" && matchAny(extra, c.Type) {
			cat = "custom"
		}
		if cat == "" && protected[typeAndName(c.Address)] {
			cat = "protected"
		}
		if cat == "" {
			continue
		}
		verb := "will be destroyed"
		if c.Action == "replace" {
			verb = "will be destroyed and recreated"
		}
		out = append(out, Danger{Address: c.Address, Action: c.Action, Category: cat, Message: c.Address + " " + verb + ". " + consequence[cat]})
	}
	return out
}

// ProtectedResources scans every .tf file under root for resources with
// lifecycle { prevent_destroy = true } and returns their "type.name" keys.
func ProtectedResources(root string) map[string]bool {
	out := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".terraform", ".groundwork", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".tf") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		f, _ := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
		if f == nil {
			return nil
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			return nil
		}
		for _, b := range body.Blocks {
			if b.Type != "resource" || len(b.Labels) != 2 {
				continue
			}
			for _, inner := range b.Body.Blocks {
				if inner.Type != "lifecycle" {
					continue
				}
				if a, ok := inner.Body.Attributes["prevent_destroy"]; ok {
					if v, diags := a.Expr.Value(nil); !diags.HasErrors() && v.True() {
						out[b.Labels[0]+"."+b.Labels[1]] = true
					}
				}
			}
		}
		return nil
	})
	return out
}
