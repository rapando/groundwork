package cli

import (
	"strings"
	"testing"

	"github.com/rapando/groundwork/v2/internal/auth"
)

func TestAuthStatusAndReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROUNDWORK_HOME", home)
	out, err := runRoot(t, "", "auth", "status")
	if err != nil || !strings.Contains(out, "not set up") {
		t.Fatalf("%v\n%s", err, out)
	}
	a, _ := auth.New(home, "")
	if err := a.SetupPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	out, err = runRoot(t, "", "auth", "status")
	if err != nil || !strings.Contains(out, "password: set") || !strings.Contains(out, "passkeys: 0") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err = runRoot(t, "n\n", "auth", "reset"); err != nil || !strings.Contains(out, "aborted") || !a.Configured() {
		t.Fatalf("declined reset: %v\n%s", err, out)
	}
	if out, err = runRoot(t, "y\n", "auth", "reset"); err != nil || a.Configured() {
		t.Fatalf("reset: %v\n%s", err, out)
	}
}
