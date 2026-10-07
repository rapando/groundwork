package terraform

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Edits go through hclwrite, which keeps comments and formatting intact.

func findResource(f *hclwrite.File, typ, name string) (*hclwrite.Block, error) {
	b := f.Body().FirstMatchingBlock("resource", []string{typ, name})
	if b == nil {
		return nil, fmt.Errorf("resource %s.%s not found in file", typ, name)
	}
	return b, nil
}

func parseWritable(src []byte, filename string) (*hclwrite.File, error) {
	f, diags := hclwrite.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("can't edit %s: it has syntax errors (%s)", filename, diags.Error())
	}
	return f, nil
}

// ValueFromRendered turns a rendered scalar ("\"x\"", "3", "true") back into a
// value. Anything else (lists, maps, masked values) is not "simple".
func ValueFromRendered(s string) (cty.Value, bool) {
	switch {
	case s == "true":
		return cty.True, true
	case s == "false":
		return cty.False, true
	case strings.HasPrefix(s, `"`):
		if u, err := strconv.Unquote(s); err == nil {
			return cty.StringVal(u), true
		}
	default:
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return cty.NumberFloatVal(n), true
		}
	}
	return cty.NilVal, false
}

// SetAttribute sets a top-level argument of resource typ.name.
func SetAttribute(src []byte, filename, typ, name, attr string, v cty.Value) ([]byte, error) {
	f, err := parseWritable(src, filename)
	if err != nil {
		return nil, err
	}
	b, err := findResource(f, typ, name)
	if err != nil {
		return nil, err
	}
	b.Body().SetAttributeValue(attr, v)
	return f.Bytes(), nil
}

// AddIgnoreChanges adds attr to lifecycle { ignore_changes = [...] } of
// resource typ.name, creating the block or list if needed.
func AddIgnoreChanges(src []byte, filename, typ, name, attr string) ([]byte, error) {
	f, err := parseWritable(src, filename)
	if err != nil {
		return nil, err
	}
	b, err := findResource(f, typ, name)
	if err != nil {
		return nil, err
	}
	lc := b.Body().FirstMatchingBlock("lifecycle", nil)
	if lc == nil {
		lc = b.Body().AppendNewBlock("lifecycle", nil)
	}
	var names []string
	if a := lc.Body().GetAttribute("ignore_changes"); a != nil {
		raw := string(a.Expr().BuildTokens(nil).Bytes())
		expr, diags := hclsyntax.ParseExpression([]byte(raw), filename, hcl.InitialPos)
		if diags.HasErrors() {
			return nil, fmt.Errorf("can't read the existing ignore_changes")
		}
		switch e := expr.(type) {
		case *hclsyntax.ScopeTraversalExpr:
			if e.Traversal.RootName() == "all" {
				return src, nil // already ignores everything
			}
		case *hclsyntax.TupleConsExpr:
			for _, item := range e.Exprs {
				tr, d := hcl.AbsTraversalForExpr(item)
				if d.HasErrors() {
					return nil, fmt.Errorf("can't read the existing ignore_changes")
				}
				names = append(names, string(hclwrite.TokensForTraversal(tr).Bytes()))
			}
		}
	}
	for _, n := range names {
		if n == attr {
			return src, nil
		}
	}
	names = append(names, attr)
	elems := make([]hclwrite.Tokens, len(names))
	for i, n := range names {
		elems[i] = hclwrite.Tokens{{Type: hclsyntax.TokenIdent, Bytes: []byte(n)}}
	}
	lc.Body().SetAttributeRaw("ignore_changes", hclwrite.TokensForTuple(elems))
	return f.Bytes(), nil
}

// RootAttr returns the top-level argument of an attribute path:
// tags.ManagedBy → tags, route[1].cidr_block → route.
func RootAttr(p string) string {
	for i, r := range p {
		if r == '.' || r == '[' {
			return p[:i]
		}
	}
	return p
}
