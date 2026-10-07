// Package vars resolves variable values per environment, finds secrets and
// scans for plaintext ones.
package vars

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

const Masked = "••••"

type Loc struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Decl is a `variable` block.
type Decl struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Default     string   `json:"default,omitempty"`
	HasDefault  bool     `json:"has_default"`
	Sensitive   bool     `json:"sensitive"`
	Validation  []string `json:"validation,omitempty"`
	Declared    Loc      `json:"declared"`
	UsedIn      []Loc    `json:"used_in"`
}

// Cell is the value of one variable in one root/environment.
type Cell struct {
	Root     string   `json:"root"`
	Env      string   `json:"env"`
	State    string   `json:"state"` // set | default | env | secret | missing | absent
	Value    string   `json:"value"`
	Source   string   `json:"source"` // "default", a file name, or "environment"
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Writable []string `json:"writable"` // tfvars files a new value may be written to
}

type Row struct {
	Name      string `json:"name"`
	Decl      *Decl  `json:"decl,omitempty"`
	Cells     []Cell `json:"cells"`
	Differs   bool   `json:"differs"`
	Missing   bool   `json:"missing"`
	Unused    bool   `json:"unused"`
	Sensitive bool   `json:"sensitive"`
}

// Target is one root in one environment.
type Target struct {
	Root     string // repo-relative
	Env      string
	VarFiles []string // repo-relative, in -var-file order
}

// Render formats a value the way it would be written in HCL.
func Render(v cty.Value) string {
	if v.IsNull() {
		return "null"
	}
	if !v.IsWhollyKnown() {
		return "(unknown)"
	}
	b, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return "?"
	}
	s := string(b)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func typeString(e hcl.Expression, src []byte) string {
	if e == nil {
		return "any"
	}
	r := e.Range()
	if r.End.Byte <= len(src) && r.Start.Byte < r.End.Byte {
		return strings.TrimSpace(string(src[r.Start.Byte:r.End.Byte]))
	}
	return "any"
}

func parseFile(path string) (*hclsyntax.Body, []byte) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	f, _ := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if f == nil {
		return nil, src
	}
	b, _ := f.Body.(*hclsyntax.Body)
	return b, src
}

// Declarations reads the variable blocks of a root and where each is used.
func Declarations(repoRoot, rootDir string) map[string]*Decl {
	rel := func(p string) string { r, _ := filepath.Rel(repoRoot, p); return filepath.ToSlash(r) }
	out := map[string]*Decl{}
	files, _ := filepath.Glob(filepath.Join(rootDir, "*.tf"))
	sort.Strings(files)
	uses := map[string][]Loc{}
	for _, f := range files {
		body, src := parseFile(f)
		if body == nil {
			continue
		}
		for _, b := range body.Blocks {
			if b.Type == "variable" && len(b.Labels) == 1 {
				d := &Decl{Name: b.Labels[0], Type: "any", Declared: Loc{rel(f), b.TypeRange.Start.Line}, UsedIn: []Loc{}}
				if a, ok := b.Body.Attributes["type"]; ok {
					d.Type = typeString(a.Expr, src)
				}
				if a, ok := b.Body.Attributes["description"]; ok {
					if v, dg := a.Expr.Value(nil); !dg.HasErrors() && v.Type() == cty.String {
						d.Description = v.AsString()
					}
				}
				if a, ok := b.Body.Attributes["default"]; ok {
					if v, dg := a.Expr.Value(nil); !dg.HasErrors() {
						d.HasDefault, d.Default = true, Render(v)
					}
				}
				if a, ok := b.Body.Attributes["sensitive"]; ok {
					if v, dg := a.Expr.Value(nil); !dg.HasErrors() && v.Type() == cty.Bool {
						d.Sensitive = v.True()
					}
				}
				for _, vb := range b.Body.Blocks {
					if vb.Type == "validation" {
						if a, ok := vb.Body.Attributes["condition"]; ok {
							d.Validation = append(d.Validation, typeString(a.Expr, src))
						}
					}
				}
				out[d.Name] = d
			}
		}
		// var.X references anywhere in the file
		_ = hclsyntax.VisitAll(body, func(n hclsyntax.Node) hcl.Diagnostics {
			if st, ok := n.(*hclsyntax.ScopeTraversalExpr); ok && st.Traversal.RootName() == "var" && len(st.Traversal) > 1 {
				if a, ok := st.Traversal[1].(hcl.TraverseAttr); ok {
					uses[a.Name] = append(uses[a.Name], Loc{rel(f), st.SrcRange.Start.Line})
				}
			}
			return nil
		})
	}
	for name, d := range out {
		d.UsedIn = append(d.UsedIn, uses[name]...)
	}
	return out
}

// assignment is a value set in a tfvars file.
type assignment struct {
	value cty.Value
	file  string
	line  int
}

func readTfvars(path string) map[string]assignment {
	out := map[string]assignment{}
	if strings.HasSuffix(path, ".json") {
		b, err := os.ReadFile(path)
		if err != nil {
			return out
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(b, &raw) != nil {
			return out
		}
		for k, msg := range raw {
			t, err := ctyjson.ImpliedType(msg)
			if err != nil {
				continue
			}
			if v, err := ctyjson.Unmarshal(msg, t); err == nil {
				out[k] = assignment{value: v, file: path, line: 1}
			}
		}
		return out
	}
	body, _ := parseFile(path)
	if body == nil {
		return out
	}
	for name, a := range body.Attributes {
		if v, d := a.Expr.Value(nil); !d.HasErrors() {
			out[name] = assignment{value: v, file: path, line: a.SrcRange.Start.Line}
		}
	}
	return out
}

// sourcesFor lists the files that set variables for a target, lowest
// precedence first, exactly as terraform loads them: terraform.tfvars,
// terraform.tfvars.json, *.auto.tfvars(.json) in lexical order, then
// -var-file in the order given. (TF_VAR_* environment variables rank below
// all of these; defaults below everything.)
func sourcesFor(repoRoot string, t Target) []string {
	dir := filepath.Join(repoRoot, filepath.FromSlash(t.Root))
	var files []string
	for _, n := range []string{"terraform.tfvars", "terraform.tfvars.json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			files = append(files, filepath.Join(dir, n))
		}
	}
	auto, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars"))
	autoJSON, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars.json"))
	auto = append(auto, autoJSON...)
	sort.Slice(auto, func(i, j int) bool { return filepath.Base(auto[i]) < filepath.Base(auto[j]) })
	files = append(files, auto...)
	for _, vf := range t.VarFiles {
		files = append(files, filepath.Join(repoRoot, filepath.FromSlash(vf)))
	}
	return files
}

// writableFor: where a value for this target may be written.
func writableFor(repoRoot string, t Target) []string {
	rel := func(p string) string { r, _ := filepath.Rel(repoRoot, p); return filepath.ToSlash(r) }
	dir := filepath.Join(repoRoot, filepath.FromSlash(t.Root))
	var out []string
	if len(t.VarFiles) > 0 {
		for _, vf := range t.VarFiles {
			out = append(out, vf) // workspace-style roots: the env's own file first
		}
	}
	out = append(out, rel(filepath.Join(dir, "terraform.tfvars")))
	auto, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars"))
	sort.Strings(auto)
	for _, a := range auto {
		out = append(out, rel(a))
	}
	return out
}

// Resolve computes one cell. environ is os.Environ()-style (for TF_VAR_*).
func Resolve(repoRoot string, t Target, d *Decl, environ []string) Cell {
	rel := func(p string) string { r, _ := filepath.Rel(repoRoot, p); return filepath.ToSlash(r) }
	c := Cell{Root: t.Root, Env: t.Env, State: "absent", Writable: writableFor(repoRoot, t)}
	if d == nil {
		return c
	}
	if d.HasDefault {
		c.State, c.Value, c.Source = "default", d.Default, "default"
		c.File, c.Line = d.Declared.File, d.Declared.Line
	}
	for _, kv := range environ {
		if k, _, ok := strings.Cut(kv, "="); ok && k == "TF_VAR_"+d.Name {
			c.State, c.Value, c.Source, c.File, c.Line = "env", Masked, "environment (TF_VAR_"+d.Name+")", "", 0
		}
	}
	for _, f := range sourcesFor(repoRoot, t) {
		if a, ok := readTfvars(f)[d.Name]; ok {
			c.State, c.Value, c.Source, c.File, c.Line = "set", Render(a.value), filepath.Base(f), rel(f), a.line
		}
	}
	if c.State == "absent" {
		c.State, c.Value, c.Source = "missing", "", "required"
	}
	if d.Sensitive && (c.State == "set" || c.State == "default") {
		c.State, c.Value = "secret", Masked
	}
	return c
}

// RawValue returns the unmasked value of a set or default variable (reveal).
func RawValue(repoRoot string, t Target, d *Decl) (string, error) {
	val := ""
	found := false
	if d.HasDefault {
		val, found = d.Default, true
	}
	for _, f := range sourcesFor(repoRoot, t) {
		if a, ok := readTfvars(f)[d.Name]; ok {
			val, found = Render(a.value), true
		}
	}
	if !found {
		return "", fmt.Errorf("%s has no value in %s", d.Name, t.Env)
	}
	return val, nil
}

// Matrix builds rows for a set of targets (typically one stack across envs).
func Matrix(repoRoot string, targets []Target, environ []string) []Row {
	declsByRoot := map[string]map[string]*Decl{}
	names := map[string]bool{}
	for _, t := range targets {
		if _, ok := declsByRoot[t.Root]; !ok {
			declsByRoot[t.Root] = Declarations(repoRoot, filepath.Join(repoRoot, filepath.FromSlash(t.Root)))
		}
		for n := range declsByRoot[t.Root] {
			names[n] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	rows := make([]Row, 0, len(sorted))
	for _, n := range sorted {
		row := Row{Name: n, Cells: []Cell{}}
		unused := true
		seenVals := map[string]bool{}
		for _, t := range targets {
			d := declsByRoot[t.Root][n]
			if d != nil && row.Decl == nil {
				row.Decl = d
			}
			if d != nil {
				row.Sensitive = row.Sensitive || d.Sensitive
				if len(d.UsedIn) > 0 {
					unused = false
				}
			}
			c := Resolve(repoRoot, t, d, environ)
			if c.State == "missing" {
				row.Missing = true
			}
			if c.State != "absent" {
				seenVals[c.State+c.Value] = true
			}
			row.Cells = append(row.Cells, c)
		}
		row.Differs = len(seenVals) > 1
		row.Unused = unused
		rows = append(rows, row)
	}
	return rows
}
