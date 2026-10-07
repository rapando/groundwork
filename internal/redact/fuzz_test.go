package redact

import (
	"strings"
	"testing"
)

// A registered secret never survives redaction, wherever it sits in a line.
func FuzzRedactsRegisteredSecret(f *testing.F) {
	f.Add("s3cr3t-value-123", "prefix ", " suffix")
	f.Add("hunter2hunter2", "password=", "")
	f.Add("AKIAIOSFODNN7EXAMPLE", "key: ", "\t")
	f.Fuzz(func(t *testing.T, secret, before, after string) {
		secret = strings.TrimSpace(secret) // AddSecret registers the trimmed value
		if len(secret) < 6 || strings.ContainsAny(secret, "\n\r") {
			return // too short to be registered
		}
		r := New()
		r.AddSecret(secret)
		out := r.Line(before + secret + after)
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q survived: %q", secret, out)
		}
	})
}
