// Package graph builds dependency and architecture views of Terraform code.
//
// The model comes from static analysis of the HCL (so it works without
// `terraform init`, and on unsaved editor buffers), optionally enriched with
// edges from `terraform graph`, and overlaid with the last plan and drift.
package graph

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

type Node struct {
	ID      string `json:"id"`   // address without instance keys, e.g. module.net.aws_subnet.private
	Kind    string `json:"kind"` // resource | data
	Type    string `json:"type"`
	Name    string `json:"name"`
	Module  string `json:"module,omitempty"` // module.net (empty for root)
	Count   string `json:"count,omitempty"`  // "×3", "×n" (unknown), "" (single)
	File    string `json:"file"`             // repo-relative
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Action  string `json:"action,omitempty"` // plan overlay: create | update | replace | delete
	Drift   bool   `json:"drift,omitempty"`
	Errors  int    `json:"errors,omitempty"` // diagnostics inside the block
}

// Edge points from a dependency to its dependent (data flows left → right).
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"` // the argument that holds the reference
}

type Graph struct {
	Nodes   []*Node  `json:"nodes"`
	Edges   []Edge   `json:"edges"`
	Source  string   `json:"source"` // static | terraform graph + static
	Modules []Module `json:"modules"`
	Errors  []string `json:"errors,omitempty"` // files that could not be parsed fully
}

type Module struct {
	Address string `json:"address"` // module.net ("" = root)
	Source  string `json:"source"`
	Dir     string `json:"dir"` // repo-relative
}

// ref is one reference found in an expression.
type ref struct {
	target string // resource/data node id (scoped), or "var.X" / "module.Y.Z" before resolution
	label  string // argument path that held it
}

type blockInfo struct {
	node    *Node
	refs    []ref
	count   hcl.Expression
	forEach hcl.Expression
}

type moduleScope struct {
	prefix  string               // "" or "module.a." or "module.a.module.b."
	dir     string               // absolute
	vars    map[string][]ref     // var name → refs passed in by the parent's module call
	varVals map[string]cty.Value // var name → known value (for counts)
	outputs map[string][]ref     // output name → refs inside this module
	blocks  []*blockInfo
	calls   map[string]*moduleCall // module name → call
}

type moduleCall struct {
	name   string
	source string
	args   map[string][]ref
	vals   map[string]cty.Value
	scope  *moduleScope
}

// Options for building a graph.
type Options struct {
	RepoRoot string
	RootDir  string            // absolute
	Buffers  map[string][]byte // absolute path → unsaved content, overrides disk
	VarVals  map[string]cty.Value
	MaxDepth int
}

func (o Options) read(p string) ([]byte, error) {
	if b, ok := o.Buffers[p]; ok {
		return b, nil
	}
	return os.ReadFile(p)
}

func (o Options) rel(p string) string {
	r, err := filepath.Rel(o.RepoRoot, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// Build parses the root and its local modules into a dependency graph.
func Build(o Options) *Graph {
	if o.MaxDepth == 0 {
		o.MaxDepth = 8
	}
	g := &Graph{Source: "static", Nodes: []*Node{}, Edges: []Edge{}, Modules: []Module{}}
	root := &moduleScope{dir: o.RootDir, vars: map[string][]ref{}, varVals: o.VarVals, outputs: map[string][]ref{}, calls: map[string]*moduleCall{}}
	parseScope(o, root, g, 0)
	g.Modules = append(g.Modules, Module{Address: "", Dir: o.rel(o.RootDir)})
	collectModules(o, root, g)

	nodes := map[string]*Node{}
	var all []*blockInfo
	var walk func(s *moduleScope)
	walk = func(s *moduleScope) {
		for _, b := range s.blocks {
			nodes[b.node.ID] = b.node
			all = append(all, b)
		}
		for _, c := range s.calls {
			if c.scope != nil {
				walk(c.scope)
			}
		}
	}
	walk(root)

	// resolve refs (through module vars and outputs) into edges
	seen := map[[2]string]bool{}
	var resolve func(s *moduleScope, r ref, depth int) []string
	resolve = func(s *moduleScope, r ref, depth int) []string {
		if depth > 16 {
			return nil
		}
		t := r.target
		switch {
		case strings.HasPrefix(t, "var."):
			var out []string
			for _, pr := range s.vars[strings.TrimPrefix(t, "var.")] {
				out = append(out, resolve(parentOf(root, s), pr, depth+1)...)
			}
			return out
		case strings.HasPrefix(t, "module."):
			parts := strings.SplitN(strings.TrimPrefix(t, "module."), ".", 2)
			c := s.calls[parts[0]]
			if c == nil || c.scope == nil {
				return nil
			}
			var outs []ref
			if len(parts) == 2 {
				outs = c.scope.outputs[parts[1]]
			} else {
				for _, o := range c.scope.outputs {
					outs = append(outs, o...)
				}
			}
			var out []string
			for _, or := range outs {
				out = append(out, resolve(c.scope, or, depth+1)...)
			}
			return out
		}
		id := s.prefix + t
		if _, ok := nodes[id]; ok {
			return []string{id}
		}
		return nil
	}
	var scopes []*moduleScope
	var collect func(s *moduleScope)
	collect = func(s *moduleScope) {
		scopes = append(scopes, s)
		for _, c := range s.calls {
			if c.scope != nil {
				collect(c.scope)
			}
		}
	}
	collect(root)
	for _, s := range scopes {
		for _, b := range s.blocks {
			for _, r := range b.refs {
				for _, from := range resolve(s, r, 0) {
					if from == b.node.ID {
						continue
					}
					k := [2]string{from, b.node.ID}
					if seen[k] {
						continue
					}
					seen[k] = true
					g.Edges = append(g.Edges, Edge{From: from, To: b.node.ID, Label: r.label})
				}
			}
			b.node.Count = countLabel(b, s)
		}
	}
	for _, b := range all {
		g.Nodes = append(g.Nodes, b.node)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g
}

func parentOf(root, s *moduleScope) *moduleScope {
	var find func(cur *moduleScope) *moduleScope
	find = func(cur *moduleScope) *moduleScope {
		for _, c := range cur.calls {
			if c.scope == s {
				return cur
			}
			if c.scope != nil {
				if p := find(c.scope); p != nil {
					return p
				}
			}
		}
		return nil
	}
	if p := find(root); p != nil {
		return p
	}
	return root
}

func collectModules(o Options, s *moduleScope, g *Graph) {
	names := make([]string, 0, len(s.calls))
	for n := range s.calls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := s.calls[n]
		m := Module{Address: s.prefix + "module." + n, Source: c.source}
		if c.scope != nil {
			m.Dir = o.rel(c.scope.dir)
		}
		g.Modules = append(g.Modules, m)
		if c.scope != nil {
			collectModules(o, c.scope, g)
		}
	}
}

var ignoredRoots = map[string]bool{"local": true, "each": true, "count": true, "path": true, "terraform": true, "self": true}

// refsIn returns the references in an expression.
func refsIn(e hcl.Expression, label string) []ref {
	var out []ref
	for _, tr := range e.Variables() {
		root := tr.RootName()
		if ignoredRoots[root] {
			continue
		}
		var names []string
		for _, step := range tr {
			switch s := step.(type) {
			case hcl.TraverseRoot:
				names = append(names, s.Name)
			case hcl.TraverseAttr:
				names = append(names, s.Name)
			}
		}
		switch {
		case root == "var" && len(names) >= 2:
			out = append(out, ref{"var." + names[1], label})
		case root == "data" && len(names) >= 3:
			out = append(out, ref{"data." + names[1] + "." + names[2], label})
		case root == "module" && len(names) >= 3:
			out = append(out, ref{"module." + names[1] + "." + names[2], label})
		case root == "module" && len(names) == 2:
			out = append(out, ref{"module." + names[1], label})
		case len(names) >= 2:
			out = append(out, ref{names[0] + "." + names[1], label})
		}
	}
	return out
}

// bodyRefs walks every attribute (and nested block) of a body.
func bodyRefs(b *hclsyntax.Body, prefix string) []ref {
	var out []ref
	names := make([]string, 0, len(b.Attributes))
	for n := range b.Attributes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if prefix == "" && (n == "count" || n == "for_each" || n == "provider") {
			// count/for_each still create dependencies, but aren't labels people care about
			out = append(out, refsIn(b.Attributes[n].Expr, n)...)
			continue
		}
		label := n
		if prefix != "" {
			label = prefix + "." + n
		}
		if n == "depends_on" {
			label = "depends_on"
		}
		out = append(out, refsIn(b.Attributes[n].Expr, label)...)
	}
	for _, nb := range b.Blocks {
		if nb.Type == "lifecycle" || nb.Type == "provisioner" || nb.Type == "connection" {
			continue
		}
		p := nb.Type
		if prefix != "" {
			p = prefix + "." + nb.Type
		}
		out = append(out, bodyRefs(nb.Body, p)...)
	}
	return out
}

func parseScope(o Options, s *moduleScope, g *Graph, depth int) {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.tf"))
	for p := range o.Buffers { // an unsaved new file in this dir
		if filepath.Dir(p) == s.dir && strings.HasSuffix(p, ".tf") && !contains(files, p) {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	for _, f := range files {
		src, err := o.read(f)
		if err != nil {
			continue
		}
		file, diags := hclsyntax.ParseConfig(src, f, hcl.InitialPos)
		if diags.HasErrors() {
			g.Errors = append(g.Errors, o.rel(f))
		}
		if file == nil {
			continue
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, b := range body.Blocks {
			switch b.Type {
			case "resource", "data":
				if len(b.Labels) != 2 {
					continue
				}
				id := b.Labels[0] + "." + b.Labels[1]
				if b.Type == "data" {
					id = "data." + id
				}
				n := &Node{ID: s.prefix + id, Kind: b.Type, Type: b.Labels[0], Name: b.Labels[1],
					Module: strings.TrimSuffix(s.prefix, "."), File: o.rel(f),
					Line: b.TypeRange.Start.Line, EndLine: b.Body.SrcRange.End.Line}
				bi := &blockInfo{node: n, refs: bodyRefs(b.Body, "")}
				if a, ok := b.Body.Attributes["count"]; ok {
					bi.count = a.Expr
				}
				if a, ok := b.Body.Attributes["for_each"]; ok {
					bi.forEach = a.Expr
				}
				s.blocks = append(s.blocks, bi)
			case "output":
				if len(b.Labels) == 1 {
					if a, ok := b.Body.Attributes["value"]; ok {
						s.outputs[b.Labels[0]] = refsIn(a.Expr, "")
					}
				}
			case "variable":
				if len(b.Labels) == 1 {
					if _, known := s.varVals[b.Labels[0]]; !known {
						if a, ok := b.Body.Attributes["default"]; ok {
							if v, d := a.Expr.Value(nil); !d.HasErrors() {
								if s.varVals == nil {
									s.varVals = map[string]cty.Value{}
								}
								s.varVals[b.Labels[0]] = v
							}
						}
					}
				}
			case "module":
				if len(b.Labels) != 1 {
					continue
				}
				c := &moduleCall{name: b.Labels[0], args: map[string][]ref{}, vals: map[string]cty.Value{}}
				for n, a := range b.Body.Attributes {
					if n == "source" {
						if v, d := a.Expr.Value(nil); !d.HasErrors() && v.Type() == cty.String {
							c.source = v.AsString()
						}
						continue
					}
					c.args[n] = refsIn(a.Expr, n)
					c.vals[n] = evalWith(a.Expr, s.varVals)
				}
				s.calls[c.name] = c
			}
		}
	}
	if depth >= o.MaxDepth {
		return
	}
	names := make([]string, 0, len(s.calls))
	for n := range s.calls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := s.calls[n]
		if !strings.HasPrefix(c.source, "./") && !strings.HasPrefix(c.source, "../") {
			continue // registry/git modules aren't in the repo
		}
		dir := filepath.Clean(filepath.Join(s.dir, filepath.FromSlash(c.source)))
		child := &moduleScope{prefix: s.prefix + "module." + n + ".", dir: dir, vars: c.args, varVals: map[string]cty.Value{}, outputs: map[string][]ref{}, calls: map[string]*moduleCall{}}
		for k, v := range c.vals {
			if v != cty.NilVal && v.IsKnown() {
				child.varVals[k] = v
			}
		}
		c.scope = child
		parseScope(o, child, g, depth+1)
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// evalWith evaluates an expression with known var.* values only.
func evalWith(e hcl.Expression, vars map[string]cty.Value) cty.Value {
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{}}
	if len(vars) > 0 {
		ctx.Variables["var"] = cty.ObjectVal(vars)
	}
	v, diags := e.Value(ctx)
	if diags.HasErrors() {
		return cty.NilVal
	}
	return v
}

func countLabel(b *blockInfo, s *moduleScope) string {
	expr := b.count
	isEach := false
	if expr == nil {
		expr, isEach = b.forEach, true
	}
	if expr == nil {
		return ""
	}
	v := evalWith(expr, s.varVals)
	if v == cty.NilVal || !v.IsKnown() || v.IsNull() {
		return "×n"
	}
	if isEach {
		if v.CanIterateElements() {
			return "×" + strconv.Itoa(v.LengthInt())
		}
		return "×n"
	}
	if v.Type() == cty.Number {
		bf := v.AsBigFloat()
		if i, acc := bf.Int64(); acc == 0 {
			return "×" + strconv.FormatInt(i, 10)
		}
	}
	return "×n"
}

// BaseAddress strips instance keys: module.a[0].aws_x.y["k"] → module.a.aws_x.y
func BaseAddress(addr string) string {
	var b strings.Builder
	depth := 0
	inQuote := false
	for _, r := range addr {
		switch {
		case r == '"' && depth > 0:
			inQuote = !inQuote
		case r == '[' && !inQuote:
			depth++
			continue
		case r == ']' && !inQuote:
			depth--
			continue
		}
		if depth == 0 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Overlay marks nodes with a plan action and drift.
func (g *Graph) Overlay(actions map[string]string, drifted map[string]bool) {
	rank := map[string]int{"": 0, "read": 1, "update": 2, "create": 3, "delete": 4, "replace": 5}
	for _, n := range g.Nodes {
		if a, ok := actions[n.ID]; ok && rank[a] > rank[n.Action] {
			n.Action = a
		}
		if drifted[n.ID] {
			n.Drift = true
		}
	}
}

// MarkErrors counts diagnostics (file, line) that fall inside each node's block.
func (g *Graph) MarkErrors(diags []struct {
	File string
	Line int
}) {
	for _, n := range g.Nodes {
		for _, d := range diags {
			if d.File == n.File && d.Line >= n.Line && d.Line <= n.EndLine {
				n.Errors++
			}
		}
	}
}

// VarsFromFiles reads tfvars files (later files win) into known values, used
// to resolve counts like `count = var.subnets` for a chosen environment.
func VarsFromFiles(files ...string) map[string]cty.Value {
	out := map[string]cty.Value{}
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
		for name, a := range body.Attributes {
			if v, d := a.Expr.Value(nil); !d.HasErrors() {
				out[name] = v
			}
		}
	}
	return out
}
