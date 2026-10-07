package graph

import (
	_ "embed"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed rules.yaml
var rulesYAML []byte

// Rules decide which resources are drawn as containers (networks) and which
// container a resource belongs in, from the arguments that reference it.
type Rules struct {
	Containers []string            `yaml:"containers"` // network-like resource types, outermost first
	Via        map[string][]string `yaml:"via"`        // argument name → container types it points into
}

func LoadRules() Rules {
	var r Rules
	_ = yaml.Unmarshal(rulesYAML, &r)
	return r
}

type Container struct {
	ID       string   `json:"id"` // the network resource's node id
	Label    string   `json:"label"`
	Children []string `json:"children"`
}

type Architecture struct {
	Nodes      []*Node     `json:"nodes"`
	Edges      []Edge      `json:"edges"`
	Containers []Container `json:"containers"`
	Unmapped   []string    `json:"unmapped"` // resources with no relation to anything drawn
	Errors     []string    `json:"errors,omitempty"`
}

// BuildArchitecture turns the dependency graph into a containment view: each
// network resource becomes a container, and resources that reference it
// (directly, or through a subnet-like resource) are placed inside.
func BuildArchitecture(g *Graph, rules Rules) *Architecture {
	a := &Architecture{Nodes: g.Nodes, Edges: g.Edges, Containers: []Container{}, Unmapped: []string{}, Errors: g.Errors}
	isContainer := map[string]bool{}
	for _, t := range rules.Containers {
		isContainer[t] = true
	}
	byID := map[string]*Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	in := map[string][]Edge{} // dependent → edges from its dependencies
	for _, e := range g.Edges {
		in[e.To] = append(in[e.To], e)
	}
	// parent: the container reached by following references (BFS, nearest first)
	parentOf := func(id string) string {
		seen := map[string]bool{id: true}
		frontier := []string{id}
		for depth := 0; depth < 4 && len(frontier) > 0; depth++ {
			var next []string
			for _, cur := range frontier {
				for _, e := range in[cur] {
					dep := byID[e.From]
					if dep == nil || seen[dep.ID] {
						continue
					}
					seen[dep.ID] = true
					if isContainer[dep.Type] {
						return dep.ID
					}
					// only follow edges whose argument is known to mean "lives in"
					arg := e.Label
					if i := strings.LastIndex(arg, "."); i >= 0 {
						arg = arg[i+1:]
					}
					if _, ok := rules.Via[arg]; ok {
						next = append(next, dep.ID)
					}
				}
			}
			frontier = next
		}
		return ""
	}
	children := map[string][]string{}
	connected := map[string]bool{}
	for _, e := range g.Edges {
		connected[e.From], connected[e.To] = true, true
	}
	for _, n := range g.Nodes {
		if isContainer[n.Type] {
			if _, ok := children[n.ID]; !ok {
				children[n.ID] = []string{}
			}
			continue
		}
		if p := parentOf(n.ID); p != "" {
			children[p] = append(children[p], n.ID)
		} else if !connected[n.ID] {
			a.Unmapped = append(a.Unmapped, n.ID)
		}
	}
	ids := make([]string, 0, len(children))
	for id := range children {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sort.Strings(children[id])
		n := byID[id]
		a.Containers = append(a.Containers, Container{ID: id, Label: n.Type + "." + n.Name, Children: children[id]})
	}
	return a
}
