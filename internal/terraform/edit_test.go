package terraform

import (
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

const src = `# routes
resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id # keep me

  tags = {
    ManagedBy = "terraform"
  }
}

resource "aws_route_table" "other" {
  vpc_id = "x"
}
`

func TestSetAttributeKeepsCommentsAndOtherBlocks(t *testing.T) {
	out, err := SetAttribute([]byte(src), "r.tf", "aws_route_table", "other", "vpc_id", cty.StringVal("vpc-123"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `vpc_id = "vpc-123"`) || !strings.Contains(s, "# keep me") || !strings.Contains(s, "# routes") ||
		!strings.Contains(s, "vpc_id = aws_vpc.main.id") {
		t.Fatalf("%s", s)
	}
	if _, err := SetAttribute([]byte(src), "r.tf", "aws_route_table", "nope", "x", cty.True); err == nil {
		t.Fatal("unknown resource")
	}
	if _, err := SetAttribute([]byte("resource \"a\" \"b\" {"), "r.tf", "a", "b", "x", cty.True); err == nil {
		t.Fatal("broken file must not be edited")
	}
}

func TestAddIgnoreChanges(t *testing.T) {
	out, err := AddIgnoreChanges([]byte(src), "r.tf", "aws_route_table", "public", "tags")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "lifecycle {\n    ignore_changes = [tags]\n  }") {
		t.Fatalf("%s", out)
	}
	// appending to an existing list, idempotently
	out2, _ := AddIgnoreChanges(out, "r.tf", "aws_route_table", "public", "route")
	if !strings.Contains(string(out2), "ignore_changes = [tags, route]") {
		t.Fatalf("%s", out2)
	}
	out3, _ := AddIgnoreChanges(out2, "r.tf", "aws_route_table", "public", "tags")
	if string(out3) != string(out2) {
		t.Fatal("adding an attribute twice must be a no-op")
	}
	all := strings.Replace(src, "  vpc_id = aws_vpc.main.id # keep me\n", "  vpc_id = aws_vpc.main.id # keep me\n  lifecycle {\n    ignore_changes = all\n  }\n", 1)
	if out4, _ := AddIgnoreChanges([]byte(all), "r.tf", "aws_route_table", "public", "tags"); string(out4) != all {
		t.Fatal("ignore_changes = all already covers it")
	}
}

func TestValueFromRenderedAndRootAttr(t *testing.T) {
	for in, want := range map[string]cty.Value{`"console"`: cty.StringVal("console"), "3": cty.NumberFloatVal(3), "true": cty.True} {
		got, ok := ValueFromRendered(in)
		if !ok || !got.RawEquals(want) {
			t.Errorf("%s → %#v", in, got)
		}
	}
	for _, bad := range []string{`["a"]`, `{"a":1}`, "(sensitive value)", "—"} {
		if _, ok := ValueFromRendered(bad); ok {
			t.Errorf("%s should not be simple", bad)
		}
	}
	if RootAttr("tags.ManagedBy") != "tags" || RootAttr("route[1].cidr_block") != "route" || RootAttr("ami") != "ami" {
		t.Fatal("RootAttr")
	}
}
