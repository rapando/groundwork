package greet

import "testing"

func TestMessage(t *testing.T) {
	if got := Message("prod"); got != "hello from prod\n" {
		t.Fatalf("got %q", got)
	}
}
