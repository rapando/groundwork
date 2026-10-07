package redact

import (
	"strings"
	"sync"
	"testing"
)

func TestPatterns(t *testing.T) {
	r := New()
	cases := map[string]string{
		"using key AKIAIOSFODNN7EXAMPLE now":           "using key •••• now",
		"Authorization: Bearer abc123.def456_ghi":      "Authorization: Bearer ••••",
		"export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K": "export AWS_SECRET_ACCESS_KEY=••••",
		`password = "hunter2hunter2"`:                  `password = ••••`,
		"db_password: s3cr3t":                          "db_password: ••••",
		"token=ghp_abcdefghijklmnopqrstuvwxyz0123":     "token=••••",
		"nothing sensitive here: id=i-0abc":            "nothing sensitive here: id=i-0abc",
		"Creating... [id=vpc-123]":                     "Creating... [id=vpc-123]",
	}
	for in, want := range cases {
		if got := r.Line(in); got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestKnownSecretsAndEnv(t *testing.T) {
	r := New()
	r.AddSecret("correct-horse-battery")
	r.AddSecret("x") // too short: would shred the log
	r.AddEnv([]string{"HOME=/home/sam", "MY_API_TOKEN=tok_1234567890", "AWS_SESSION_TOKEN=FwoGZXIvYXdzE", "PATH=/usr/bin", "SHORT_SECRET=ab"})
	got := r.Line("pw=correct-horse-battery and tok_1234567890 and FwoGZXIvYXdzE, x, ab, /home/sam")
	if strings.Contains(got, "correct-horse-battery") || strings.Contains(got, "tok_1234567890") || strings.Contains(got, "FwoGZXIvYXdzE") {
		t.Fatalf("leaked: %s", got)
	}
	if !strings.Contains(got, ", x, ab, /home/sam") {
		t.Fatalf("over-redacted: %s", got)
	}
	// overlapping secrets: the longer one is masked whole
	r2 := New()
	r2.AddSecret("abcdef")
	r2.AddSecret("abcdef-longer")
	if got := r2.Line("v=abcdef-longer"); strings.Contains(got, "longer") {
		t.Fatalf("partial mask leaked a suffix: %s", got)
	}
}

func TestPrivateKeyBlock(t *testing.T) {
	r := New()
	in := []string{"before", "-----BEGIN RSA PRIVATE KEY-----", "MIIEpAIBAAKCAQEA", "more+base64==", "-----END RSA PRIVATE KEY-----", "after"}
	var out []string
	for _, l := range in {
		out = append(out, r.Line(l))
	}
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "MIIE") || strings.Contains(joined, "base64") || strings.Contains(joined, "BEGIN") {
		t.Fatalf("key material leaked:\n%s", joined)
	}
	if out[0] != "before" || out[5] != "after" {
		t.Fatalf("lines outside the block must pass through: %v", out)
	}
	// state doesn't leak into the next unrelated line
	if r.Line("plain") != "plain" {
		t.Fatal("PEM state stuck")
	}
}

func TestConcurrentUse(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.AddSecret("secret-value-123")
			r.Line("hello secret-value-123")
		}()
	}
	wg.Wait()
}

func FuzzLine(f *testing.F) {
	f.Add("password=abc")
	f.Add("-----BEGIN PRIVATE KEY-----")
	f.Fuzz(func(t *testing.T, s string) {
		r := New()
		r.AddSecret("fuzz-secret-value")
		out := r.Line(s + "fuzz-secret-value")
		if strings.Contains(out, "fuzz-secret-value") {
			t.Fatalf("registered secret survived: %q", out)
		}
	})
}
