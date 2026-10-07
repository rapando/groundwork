package vars

import (
	"math"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is a likely secret committed in plain text.
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // error for unmistakable formats, warning for key=value guesses
	Message  string `json:"message"`
	Key      string `json:"key,omitempty"`
	Excerpt  string `json:"excerpt"` // the line with the value masked
}

var formats = []struct {
	rule, what string
	re         *regexp.Regexp
}{
	{"aws-access-key-id", "an AWS access key ID", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"private-key", "a private key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"github-token", "a GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"gitlab-token", "a GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"slack-token", "a Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"stripe-key", "a Stripe secret key", regexp.MustCompile(`\b[sr]k_live_[0-9A-Za-z]{20,}\b`)},
	{"google-api-key", "a Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
}

// key = "value" / key: value, for secret-looking keys
var assignRe = regexp.MustCompile(`(?i)^\s*["']?([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)[A-Za-z0-9_.-]*)["']?\s*[=:]\s*(.+?)\s*,?\s*$`)

// keys that name a file, path or setting about a secret rather than the secret
var notASecretKey = regexp.MustCompile(`(?i)(_file|_path|_dir|_name|_length|_len|_min|_max|_policy|_url|_arn|_id|_ref|_env|_enabled|_required|_rotation|_version)$`)

var placeholder = regexp.MustCompile(`(?i)^(change[-_ ]?me|todo|xxx+|\*+|<.*>|\[.*\]|example|redacted|placeholder|secret|password|none|null|true|false|~)$`)

// shannon entropy in bits per character
func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]float64{}
	for _, r := range s {
		counts[r]++
	}
	n := float64(len([]rune(s)))
	e := 0.0
	for _, c := range counts {
		p := c / n
		e -= p * math.Log2(p)
	}
	return e
}

// isReference: values that point somewhere else are not secrets.
func isReference(v string) bool {
	for _, p := range []string{"{{", "${", "var.", "local.", "data.", "module.", "!vault", "$ANSIBLE_VAULT", "lookup(", "env(", "sops", "vault:", "file("} {
		if strings.Contains(v, p) {
			return true
		}
	}
	return false
}

// Scannable reports whether a file is in scope for the plaintext scan.
func Scannable(path string) bool {
	b := filepath.Base(path)
	if b == ".terraform.lock.hcl" || strings.Contains(b, ".enc.") || strings.HasSuffix(b, ".example") {
		return false
	}
	switch filepath.Ext(path) {
	case ".tf", ".tfvars", ".yml", ".yaml", ".json", ".cfg", ".ini", ".env", ".hcl":
		return true
	}
	return b == "hosts" || b == ".env"
}

// Scan looks for secrets committed in plain text. Encrypted files are skipped.
func Scan(file string, content []byte) []Finding {
	s := string(content)
	if strings.HasPrefix(strings.TrimSpace(s), "$ANSIBLE_VAULT;") || (strings.Contains(s, "\nsops:") && strings.Contains(s, "mac:")) || strings.Contains(s, `"sops": {`) {
		return nil
	}
	var out []Finding
	inVaultBlock := false
	for i, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, "!vault") {
			inVaultBlock = true
			continue
		}
		if inVaultBlock {
			if strings.HasPrefix(trimmed, "$ANSIBLE_VAULT") || (trimmed != "" && strings.Trim(trimmed, "0123456789abcdef") == "") {
				continue
			}
			inVaultBlock = false
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		found := false
		for _, f := range formats {
			if loc := f.re.FindStringIndex(line); loc != nil {
				out = append(out, Finding{File: file, Line: i + 1, Col: loc[0] + 1, Rule: f.rule, Severity: "error",
					Message: "This looks like " + f.what + " committed in plain text.", Excerpt: line[:loc[0]] + "••••"})
				found = true
				break
			}
		}
		if found {
			continue
		}
		m := assignRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		key, raw := line[m[2]:m[3]], line[m[4]:m[5]]
		val := strings.Trim(strings.TrimSpace(raw), `"'`)
		if notASecretKey.MatchString(key) || strings.HasPrefix(val, "/") || strings.HasPrefix(val, "./") || strings.HasPrefix(val, "~/") || strings.HasPrefix(val, "../") {
			continue
		}
		if val == "" || isReference(raw) || placeholder.MatchString(val) || len(val) < 6 || entropy(val) < 2.5 {
			continue
		}
		out = append(out, Finding{File: file, Line: i + 1, Col: m[4] + 1, Rule: "secret-assignment", Severity: "warning", Key: key,
			Message: key + " looks like a secret committed in plain text.", Excerpt: line[:m[4]] + "••••"})
	}
	return out
}
