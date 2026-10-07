package store

import (
	"path/filepath"
	"testing"
)

func TestOpenMigrateKV(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, _ := s.GetKV("a"); ok {
		t.Fatal("unexpected key")
	}
	if err := s.SetKV("a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV("a", "2"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.GetKV("a"); !ok || v != "2" {
		t.Fatalf("got %q %v", v, ok)
	}
}

func TestDiagnosticsReplaceAndList(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := DiagRow{Severity: "error", Message: "boom", File: "a.tf", Line: 3, Col: 1, Detail: "d", EndLine: 3, EndCol: 9, Link: "http://x", FixJSON: `{"k":1}`}
	b := DiagRow{Severity: "warning", Message: "meh", File: "b.tf", Line: 1}
	if _, err := s.ReplaceDiagnostics("u1", "validate", "h1", []DiagRow{a, b}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceDiagnostics("u1", "tflint", "h1", []DiagRow{b}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListDiagnostics("", "", "")
	if len(all) != 3 {
		t.Fatalf("got %d", len(all))
	}
	// replacing one tool leaves the other untouched
	s.ReplaceDiagnostics("u1", "validate", "h2", nil)
	all, _ = s.ListDiagnostics("u1", "", "")
	if len(all) != 1 || all[0].Tool != "tflint" {
		t.Fatalf("%+v", all)
	}
	s.ReplaceDiagnostics("u1", "validate", "h3", []DiagRow{a})
	got, _ := s.ListDiagnostics("", "error", "a.tf")
	if len(got) != 1 || got[0].Detail != "d" || got[0].EndCol != 9 || got[0].Link != "http://x" || got[0].FixJSON != `{"k":1}` {
		t.Fatalf("%+v", got)
	}
	s.DeleteUnitsExcept([]string{"other"})
	if all, _ = s.ListDiagnostics("", "", ""); len(all) != 0 {
		t.Fatal("stale units should be dropped")
	}
}
