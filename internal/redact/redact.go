// Package redact masks secrets in job output before it is stored or shown.
package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

const Mask = "••••"

var patterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), Mask},
	{regexp.MustCompile(`(?i)\b(Bearer)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 " + Mask},
	{regexp.MustCompile(`(?i)\b(gh[pousr]_[A-Za-z0-9]{20,}|glpat-[A-Za-z0-9_-]{16,}|xox[abprs]-[A-Za-z0-9-]{10,})\b`), Mask},
	// key = value / key: value for secret-looking keys; keeps the key for context
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)[A-Za-z0-9_.-]*)(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)`), "$1$2" + Mask},
}

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// Redactor is safe for concurrent use. One instance per job keeps PEM state
// (multi-line private keys) from leaking between streams.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string // longest first so overlapping values mask fully
	inPEM   bool
}

func New() *Redactor { return &Redactor{} }

// AddSecret registers an exact value to mask everywhere. Short values are
// ignored: masking "1" or "dev" would destroy the log.
func (r *Redactor) AddSecret(v string) {
	v = strings.TrimSpace(v)
	if len(v) < 6 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if s == v {
			return
		}
	}
	r.secrets = append(r.secrets, v)
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// AddEnv registers values of secret-looking environment variables
// (AWS_SECRET_ACCESS_KEY, *_TOKEN, *_PASSWORD, ...) from KEY=VALUE pairs.
func (r *Redactor) AddEnv(environ []string) {
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		u := strings.ToUpper(k)
		for _, w := range []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "PRIVATE_KEY", "CREDENTIAL", "SESSION_TOKEN"} {
			if strings.Contains(u, w) {
				r.AddSecret(v)
				break
			}
		}
	}
}

// Line redacts one line of output.
func (r *Redactor) Line(s string) string {
	r.mu.Lock()
	if r.inPEM {
		if pemEnd.MatchString(s) {
			r.inPEM = false
		}
		r.mu.Unlock()
		return Mask
	}
	if pemBegin.MatchString(s) {
		if !pemEnd.MatchString(s) {
			r.inPEM = true
		}
		s = pemBegin.ReplaceAllString(s, Mask) // then keep going: the line may hold other secrets
	}
	secrets := append([]string(nil), r.secrets...)
	r.mu.Unlock()

	for _, sec := range secrets {
		if strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, Mask)
		}
	}
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
