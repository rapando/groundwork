package service

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSuggestDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"go/src", "go/pkg", "Google", "gone/.git", ".config", "docs"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "gofile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	paths := func(ds []DirSuggestion) []string {
		var out []string
		for _, d := range ds {
			out = append(out, d.Path)
		}
		return out
	}
	for typed, want := range map[string][]string{
		"~/go":   {"~/go", "~/gone", "~/Google"}, // folders only, any case
		"~/go/":  {"~/go/pkg", "~/go/src"},
		"~":      {"~/docs", "~/go", "~/gone", "~/Google"},
		"~/.c":   {"~/.config"}, // hidden only when asked for
		"~/nope": nil,
		"go":     nil, // relative paths mean nothing to the service
	} {
		if got := paths(SuggestDirs(typed)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", typed, got, want)
		}
	}
	abs := SuggestDirs(filepath.Join(home, "gon"))
	if len(abs) != 1 || abs[0].Path != filepath.Join(home, "gone") || !abs[0].Git {
		t.Errorf("absolute: %+v", abs)
	}
}
