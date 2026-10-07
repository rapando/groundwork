package graph

import (
	"os"
	"testing"
)

func FuzzParseDOT(f *testing.F) {
	if b, err := os.ReadFile("../../testdata/terraform/graph_plan.dot"); err == nil {
		f.Add(string(b))
	}
	f.Add(`digraph { "a" -> "b" }`)
	f.Fuzz(func(t *testing.T, dot string) { ParseDOT(dot) })
}
