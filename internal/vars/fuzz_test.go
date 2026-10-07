package vars

import (
	"strings"
	"testing"
)

func FuzzScan(f *testing.F) {
	f.Add("db_password = \"Sup3r-S3cret!x\"\naccess_key = \"AKIAIOSFODNN7EXAMPLE\"\n")
	f.Add("api_token: \"{{ vault_api_token }}\"\n")
	f.Add("tls_key: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  6161\n")
	f.Fuzz(func(t *testing.T, src string) {
		for _, fd := range Scan("x.tfvars", []byte(src)) {
			if fd.Line < 1 || strings.Contains(fd.Excerpt, "\n") {
				t.Fatalf("bad finding %+v", fd)
			}
		}
	})
}

// Any literal the editor accepts writes back as HCL that reads the same value.
func FuzzLiteralRoundTrip(f *testing.F) {
	for _, s := range []string{`"x"`, `3`, `true`, `["a", "b"]`, `{ k = "v" }`, `"multi\nline"`, `null`, `-1.5`, `"${x}"`, `"%{if}"`, `[1/0]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		v, err := ParseLiteral(text)
		if err != nil {
			return
		}
		out, err := SetValue(nil, "f.tfvars", "v", v)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseLiteral(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "v =")))
		if err != nil {
			t.Fatalf("%q wrote %q which doesn't parse: %v", text, out, err)
		}
		if !back.RawEquals(v) && (!v.IsNull() || !back.IsNull()) {
			if !back.Equals(v).True() {
				t.Fatalf("%q round-tripped to %#v (was %#v) via %q", text, back, v, out)
			}
		}
	})
}
