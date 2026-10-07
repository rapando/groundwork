package graph

import (
	"regexp"
	"strings"
)

var (
	dotNode = regexp.MustCompile(`^\s*"([^"]+)"\s*\[.*shape\s*=\s*"(\w+)"`)
	dotEdge = regexp.MustCompile(`^\s*"((?:[^"\\]|\\.)+)"\s*->\s*"((?:[^"\\]|\\.)+)"`)
)

func dotID(raw string) string {
	s := strings.TrimPrefix(raw, "[root] ")
	for _, suf := range []string{" (expand)", " (close)", " (orphan)"} {
		s = strings.TrimSuffix(s, suf)
	}
	return BaseAddress(s)
}

// ParseDOT reads `terraform graph -type=plan` output and returns
// dependency → dependent edges between resources. Terraform routes many
// dependencies through variable, output and module nodes, so edges are taken
// transitively through any non-resource node.
func ParseDOT(dot string) []Edge {
	resources := map[string]bool{}
	deps := map[string][]string{} // node → nodes it depends on
	for _, l := range strings.Split(dot, "\n") {
		if m := dotNode.FindStringSubmatch(l); m != nil {
			if m[2] == "box" {
				resources[dotID(m[1])] = true
			}
			continue
		}
		if m := dotEdge.FindStringSubmatch(l); m != nil {
			a, b := dotID(m[1]), dotID(m[2])
			if a != b {
				deps[a] = append(deps[a], b)
			}
		}
	}
	var out []Edge
	seen := map[[2]string]bool{}
	for r := range resources {
		// walk through non-resource nodes to the nearest resources
		visited := map[string]bool{r: true}
		stack := append([]string(nil), deps[r]...)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if visited[n] {
				continue
			}
			visited[n] = true
			if resources[n] {
				k := [2]string{n, r}
				if !seen[k] {
					seen[k] = true
					out = append(out, Edge{From: n, To: r})
				}
				continue
			}
			stack = append(stack, deps[n]...)
		}
	}
	return out
}

// MergeDOT adds edges terraform knows about (implicit provider/module ordering
// aside) that static analysis missed, for nodes that exist in the graph.
func (g *Graph) MergeDOT(edges []Edge) {
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	have := map[[2]string]bool{}
	for _, e := range g.Edges {
		have[[2]string{e.From, e.To}] = true
	}
	for _, e := range edges {
		if ids[e.From] && ids[e.To] && !have[[2]string{e.From, e.To}] {
			g.Edges = append(g.Edges, e)
			have[[2]string{e.From, e.To}] = true
		}
	}
	g.Source = "terraform graph + static analysis"
}
