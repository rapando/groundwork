package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFirstUseIsOncePerMachine(t *testing.T) {
	home := t.TempDir()
	svc, err := New(context.Background(), home, "test", quiet())
	if err != nil {
		t.Fatal(err)
	}
	if !svc.FirstUse() {
		t.Fatal("a fresh home is first use")
	}
	if svc.FirstUse() {
		t.Fatal("first use happens once")
	}
	if _, err := os.Stat(filepath.Join(home, onboardingFile)); err != nil {
		t.Fatal(err)
	}
	// a restarted service (or another project) remembers it
	again, err := New(context.Background(), home, "test", quiet())
	if err != nil {
		t.Fatal(err)
	}
	if again.FirstUse() {
		t.Fatal("remembered across restarts")
	}
}
