package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func labOpts(t *testing.T) Options {
	t.Helper()
	repo, _ := filepath.Abs("../../testdata/repos/graph-lab")
	root := filepath.Join(repo, "envs/dev")
	return Options{RepoRoot: repo, RootDir: root, VarVals: VarsFromFiles(filepath.Join(root, "terraform.tfvars"))}
}

func edgeSet(g *Graph) map[string]string {
	m := map[string]string{}
	for _, e := range g.Edges {
		m[e.From+" -> "+e.To] = e.Label
	}
	return m
}

func TestStaticGraphFollowsModulesAndCounts(t *testing.T) {
	g := Build(labOpts(t))
	nodes := map[string]*Node{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	for _, id := range []string{"aws_vpc.main", "aws_subnet.private", "data.aws_ami.ubuntu", "module.app.aws_instance.web", "aws_route53_record.app", "aws_iam_role.ops"} {
		if nodes[id] == nil {
			t.Fatalf("missing node %s in %v", id, g.Nodes)
		}
	}
	if nodes["aws_subnet.private"].Count != "×3" {
		t.Errorf("count from tfvars: %q", nodes["aws_subnet.private"].Count)
	}
	if nodes["module.app.aws_instance.web"].Count != "×2" {
		t.Errorf("count from a literal module argument: %q", nodes["module.app.aws_instance.web"].Count)
	}
	if n := nodes["module.app.aws_instance.web"]; n.File != "modules/app/main.tf" || n.Line != 14 || n.EndLine != 19 || n.Module != "module.app" {
		t.Errorf("location: %+v", n)
	}
	es := edgeSet(g)
	want := map[string]string{
		"aws_vpc.main -> aws_subnet.private":                    "vpc_id",
		"aws_vpc.main -> aws_internet_gateway.gw":               "vpc_id",
		"aws_subnet.private -> module.app.aws_instance.web":     "subnet_id", // through var.subnet_ids
		"data.aws_ami.ubuntu -> module.app.aws_instance.web":    "ami",
		"module.app.aws_instance.web -> aws_route53_record.app": "records", // through the module output
		"aws_internet_gateway.gw -> aws_route53_record.app":     "depends_on",
	}
	for e, label := range want {
		got, ok := es[e]
		if !ok {
			t.Errorf("missing edge %s", e)
		} else if got != label {
			t.Errorf("%s labelled %q, want %q", e, got, label)
		}
	}
	if len(es) != len(want) {
		t.Errorf("unexpected edges: %v", es)
	}
}

func TestUnknownCountAndVarlessRoot(t *testing.T) {
	o := labOpts(t)
	o.VarVals = nil // no tfvars chosen: subnets has no default
	g := Build(o)
	for _, n := range g.Nodes {
		if n.ID == "aws_subnet.private" && n.Count != "×n" {
			t.Fatalf("unknown count should be ×n, got %q", n.Count)
		}
	}
}

func TestBuffersOverrideDiskAndBrokenFilesStillParse(t *testing.T) {
	o := labOpts(t)
	mod := filepath.Join(o.RepoRoot, "modules/app/main.tf")
	b, _ := os.ReadFile(mod)
	// an unsaved edit: the instance no longer uses the subnets, and the file has a syntax error at the end
	edited := strings.Replace(string(b), "  subnet_id     = var.subnet_ids[0]\n", "", 1) + "\nresource \"broken\" {\n"
	o.Buffers = map[string][]byte{mod: []byte(edited)}
	g := Build(o)
	if _, ok := edgeSet(g)["aws_subnet.private -> module.app.aws_instance.web"]; ok {
		t.Fatal("the unsaved buffer must win over the file on disk")
	}
	if len(g.Errors) != 1 || g.Errors[0] != "modules/app/main.tf" {
		t.Fatalf("parse errors should be reported: %v", g.Errors)
	}
	found := false
	for _, n := range g.Nodes {
		found = found || n.ID == "module.app.aws_instance.web"
	}
	if !found {
		t.Fatal("blocks before a syntax error still appear")
	}
}

func TestArchitectureContainment(t *testing.T) {
	a := BuildArchitecture(Build(labOpts(t)), LoadRules())
	if len(a.Containers) != 1 || a.Containers[0].ID != "aws_vpc.main" {
		t.Fatalf("%+v", a.Containers)
	}
	kids := strings.Join(a.Containers[0].Children, ",")
	if kids != "aws_internet_gateway.gw,aws_subnet.private,module.app.aws_instance.web" {
		t.Fatalf("children: %s (the instance is in the VPC through its subnet)", kids)
	}
	if strings.Join(a.Unmapped, ",") != "aws_iam_role.ops" {
		t.Fatalf("unmapped: %v", a.Unmapped)
	}
}

func TestParseRealDOT(t *testing.T) {
	b, err := os.ReadFile("../../testdata/terraform/graph_plan.dot")
	if err != nil {
		t.Fatal(err)
	}
	if es := ParseDOT(string(b)); len(es) != 0 {
		t.Fatalf("one resource, no resource edges: %v", es)
	}
	// terraform routes dependencies through var/output/module nodes
	dot := `digraph {
	subgraph "root" {
		"[root] aws_vpc.main (expand)" [label = "aws_vpc.main", shape = "box"]
		"[root] module.app.aws_instance.web (expand)" [label = "module.app.aws_instance.web", shape = "box"]
		"[root] module.app.var.subnet (expand)" [label = "module.app.var.subnet", shape = "note"]
		"[root] module.app.aws_instance.web (expand)" -> "[root] module.app.var.subnet (expand)"
		"[root] module.app.var.subnet (expand)" -> "[root] aws_vpc.main (expand)"
		"[root] aws_vpc.main (expand)" -> "[root] provider[\"registry.terraform.io/hashicorp/aws\"]"
	}
}`
	es := ParseDOT(dot)
	if len(es) != 1 || es[0].From != "aws_vpc.main" || es[0].To != "module.app.aws_instance.web" {
		t.Fatalf("%v", es)
	}
	g := &Graph{Nodes: []*Node{{ID: "aws_vpc.main"}, {ID: "module.app.aws_instance.web"}}}
	g.MergeDOT(es)
	if len(g.Edges) != 1 || g.Source != "terraform graph + static analysis" {
		t.Fatalf("%+v", g)
	}
}

func TestBaseAddressAndOverlay(t *testing.T) {
	for in, want := range map[string]string{
		`module.a[0].aws_x.y["k[1]"]`: "module.a.aws_x.y",
		"aws_subnet.private[2]":       "aws_subnet.private",
		"data.aws_ami.x":              "data.aws_ami.x",
	} {
		if got := BaseAddress(in); got != want {
			t.Errorf("%s → %s", in, got)
		}
	}
	g := Build(labOpts(t))
	g.Overlay(map[string]string{"aws_subnet.private": "update", "aws_vpc.main": "replace"}, map[string]bool{"aws_internet_gateway.gw": true})
	for _, n := range g.Nodes {
		switch n.ID {
		case "aws_subnet.private":
			if n.Action != "update" {
				t.Error(n.Action)
			}
		case "aws_internet_gateway.gw":
			if !n.Drift {
				t.Error("drift overlay")
			}
		}
	}
}
