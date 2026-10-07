package vars

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// ParseLiteral parses a value typed by the user as an HCL literal
// ("x", 3, true, ["a"], { k = "v" }). References and function calls are refused.
func ParseLiteral(text string) (cty.Value, error) {
	expr, diags := hclsyntax.ParseExpression([]byte(text), "value", hcl.InitialPos)
	if diags.HasErrors() {
		return cty.NilVal, fmt.Errorf("not a valid value: %s (strings need quotes)", diags[0].Summary)
	}
	if st, ok := expr.(*hclsyntax.ScopeTraversalExpr); ok && len(st.Traversal) == 1 {
		return cty.NilVal, fmt.Errorf("%s isn't a value: strings need quotes (\"%s\")", text, st.Traversal.RootName())
	}
	if len(expr.Variables()) > 0 {
		return cty.NilVal, fmt.Errorf("references aren't allowed here; enter a literal value")
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() {
		return cty.NilVal, fmt.Errorf("not a literal value: %s", diags[0].Summary)
	}
	// 1/0 evaluates to infinity, which HCL can't write back
	err := cty.Walk(v, func(_ cty.Path, x cty.Value) (bool, error) {
		if x.Type() == cty.Number && x.IsKnown() && !x.IsNull() && x.AsBigFloat().IsInf() {
			return false, fmt.Errorf("not a literal value: %s is infinite", text)
		}
		return true, nil
	})
	if err != nil {
		return cty.NilVal, err
	}
	return v, nil
}

// SetValue sets name = v in a tfvars file, keeping comments and other lines.
func SetValue(src []byte, filename, name string, v cty.Value) ([]byte, error) {
	f, diags := hclwrite.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s has syntax errors; fix it first", filename)
	}
	f.Body().SetAttributeValue(name, v)
	return f.Bytes(), nil
}
