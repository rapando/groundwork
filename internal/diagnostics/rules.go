// Package diagnostics turns run logs and check findings into Issues: known
// problems with an explanation and a way to fix them.
package diagnostics

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/rapando/groundwork/internal/checks"
)

//go:embed rules/*.yaml
var builtin embed.FS

type DiagMatch struct {
	Tool    string `yaml:"tool"`
	Code    string `yaml:"code"`
	Message string `yaml:"message"`
}

type Matcher struct {
	Log        string     `yaml:"log"`
	Diagnostic *DiagMatch `yaml:"diagnostic"`
}

type Action struct {
	Kind     string            `yaml:"kind" json:"kind"` // run | open | copy
	Label    string            `yaml:"label" json:"label"`
	Run      map[string]string `yaml:"run" json:"-"`
	Href     string            `yaml:"href" json:"href,omitempty"`
	Text     string            `yaml:"text" json:"text,omitempty"`
	Command  string            `yaml:"command" json:"command,omitempty"` // shown for transparency
	Confirm  string            `yaml:"confirm" json:"confirm,omitempty"` // text the user types
	Mutating bool              `yaml:"mutating" json:"mutating,omitempty"`
}

type Step struct {
	Title  string  `yaml:"title" json:"title"`
	Detail string  `yaml:"detail" json:"detail,omitempty"`
	Action *Action `yaml:"action" json:"action,omitempty"`
}

type Rule struct {
	ID         string                  `yaml:"id"`
	Scope      string                  `yaml:"scope"` // terraform | ansible | any
	Category   string                  `yaml:"category"`
	Severity   string                  `yaml:"severity"`
	Supersedes []string                `yaml:"supersedes"`
	Each       string                  `yaml:"each"` // one issue per match; named groups become variables (host → .Host)
	Match      struct{ Any []Matcher } `yaml:"match"`
	Extract    map[string]string       `yaml:"extract"`
	Title      string                  `yaml:"title"`
	Explain    string                  `yaml:"explain"`
	Checks     []map[string]string     `yaml:"checks"`
	Steps      []Step                  `yaml:"steps"`
	Resolve    string                  `yaml:"resolve"`     // success | diagnostic | manual
	ResolvedBy []string                `yaml:"resolved_by"` // run kinds whose success clears it (default: plan/drift, or any ans.*)
	Origin     string                  `yaml:"-"`

	logRes  []*regexp.Regexp
	diagRes [][3]*regexp.Regexp
	each    *regexp.Regexp
	extract map[string]*regexp.Regexp
}

func (r *Rule) compile() error {
	if r.ID == "" || r.Title == "" || len(r.Match.Any) == 0 {
		return fmt.Errorf("rule %q: id, title and match.any are required", r.ID)
	}
	if r.Scope == "" {
		r.Scope = "any"
	}
	if r.Resolve == "" {
		r.Resolve = "success"
	}
	re := func(s string) (*regexp.Regexp, error) {
		if s == "" {
			return nil, nil
		}
		x, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.ID, err)
		}
		return x, nil
	}
	for _, m := range r.Match.Any {
		if m.Log != "" {
			x, err := re(m.Log)
			if err != nil {
				return err
			}
			r.logRes = append(r.logRes, x)
		}
		if d := m.Diagnostic; d != nil {
			var t [3]*regexp.Regexp
			for i, s := range []string{d.Tool, d.Code, d.Message} {
				x, err := re(s)
				if err != nil {
					return err
				}
				t[i] = x
			}
			if d.Tool != "" {
				t[0] = regexp.MustCompile("^" + regexp.QuoteMeta(d.Tool) + "$")
			}
			r.diagRes = append(r.diagRes, t)
		}
	}
	var err error
	if r.each, err = re(r.Each); err != nil {
		return err
	}
	r.extract = map[string]*regexp.Regexp{}
	for k, s := range r.Extract {
		if r.extract[k], err = re(s); err != nil {
			return err
		}
	}
	// templates must parse now, not when an issue is shown
	for _, t := range r.templates() {
		if _, err := template.New("").Funcs(funcs).Parse(t); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
	}
	return nil
}

func (r *Rule) templates() []string {
	out := []string{r.Title, r.Explain}
	for _, s := range r.Steps {
		out = append(out, s.Title, s.Detail)
		if a := s.Action; a != nil {
			out = append(out, a.Label, a.Href, a.Text, a.Command, a.Confirm)
			for _, v := range a.Run {
				out = append(out, v)
			}
		}
	}
	for _, c := range r.Checks {
		for _, v := range c {
			out = append(out, v)
		}
	}
	return out
}

func parseRules(b []byte, origin string) ([]*Rule, error) {
	var rs []*Rule
	if err := yaml.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("%s: %w", origin, err)
	}
	for _, r := range rs {
		r.Origin = origin
		if err := r.compile(); err != nil {
			return nil, fmt.Errorf("%s: %w", origin, err)
		}
	}
	return rs, nil
}

// LoadRules reads the built-in rules, then .groundwork/rules/*.yaml from
// userDir: a user rule with a built-in's ID replaces it. Broken user files are
// reported and skipped; built-ins must always load.
func LoadRules(userDir string) ([]*Rule, []error) {
	byID := map[string]*Rule{}
	var order []string
	add := func(rs []*Rule) {
		for _, r := range rs {
			if _, ok := byID[r.ID]; !ok {
				order = append(order, r.ID)
			}
			byID[r.ID] = r
		}
	}
	entries, _ := fs.Glob(builtin, "rules/*.yaml")
	sort.Strings(entries)
	for _, e := range entries {
		b, _ := builtin.ReadFile(e)
		rs, err := parseRules(b, "builtin:"+e)
		if err != nil {
			panic(err) // a broken built-in rule is a build error; rules_test catches it
		}
		add(rs)
	}
	var errs []error
	if userDir != "" {
		files, _ := filepath.Glob(filepath.Join(userDir, "*.yaml"))
		sort.Strings(files)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			rs, err := parseRules(b, f)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			add(rs)
		}
	}
	out := make([]*Rule, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, errs
}

// Hit is one rule matching one thing (a run's log, a host in it, a finding).
type Hit struct {
	Rule    *Rule
	Key     string            // host, or file:line for findings; "" when one issue per target
	Vars    map[string]string // extracted values
	Excerpt string            // the matching part of the log
}

func excerpt(text string, loc []int) string {
	start, end := loc[0], loc[1]
	// widen to whole lines, a few on each side
	for i := 0; i < 3 && start > 0; i++ {
		if p := strings.LastIndexByte(text[:start-1], '\n'); p >= 0 {
			start = p + 1
		} else {
			start = 0
		}
	}
	for i := 0; i < 12 && end < len(text); i++ {
		if p := strings.IndexByte(text[end+1:], '\n'); p >= 0 {
			end = end + 1 + p
		} else {
			end = len(text)
		}
	}
	s := strings.TrimSpace(text[start:end])
	if len(s) > 4000 {
		s = s[:4000] + "…"
	}
	return s
}

func extractAll(r *Rule, text string, vars map[string]string) {
	for k, x := range r.extract {
		if _, set := vars[k]; set {
			continue
		}
		vars[k] = ""
		if m := x.FindStringSubmatch(text); len(m) > 1 {
			vars[k] = strings.TrimSpace(m[1])
		}
	}
}

// MatchLog runs log rules of the given scope (terraform | ansible) over text.
func MatchLog(rules []*Rule, scope, text string) []Hit {
	var hits []Hit
	for _, r := range rules {
		if r.Scope != "any" && r.Scope != scope {
			continue
		}
		var loc []int
		for _, x := range r.logRes {
			if loc = x.FindStringIndex(text); loc != nil {
				break
			}
		}
		if loc == nil {
			continue
		}
		if r.each == nil {
			vars := map[string]string{}
			extractAll(r, text, vars)
			hits = append(hits, Hit{Rule: r, Vars: vars, Excerpt: excerpt(text, loc)})
			continue
		}
		seen := map[string]bool{}
		for _, m := range r.each.FindAllStringSubmatchIndex(text, -1) {
			vars := map[string]string{}
			for i, name := range r.each.SubexpNames() {
				if name != "" && m[2*i] >= 0 {
					vars[name] = text[m[2*i]:m[2*i+1]]
				}
			}
			var parts []string // the key: named groups' values, e.g. the host
			for _, name := range r.each.SubexpNames() {
				if name != "" {
					parts = append(parts, vars[name])
				}
			}
			key := strings.Join(parts, " ")
			if seen[key] {
				continue
			}
			seen[key] = true
			block := text[m[0]:m[1]]
			extractAll(r, block, vars)
			extractAll(r, text, vars)
			hits = append(hits, Hit{Rule: r, Key: key, Vars: vars, Excerpt: excerpt(text, []int{m[0], m[1]})})
		}
	}
	return supersede(hits)
}

func supersede(hits []Hit) []Hit {
	drop := map[string]bool{}
	for _, h := range hits {
		for _, id := range h.Rule.Supersedes {
			drop[id] = true
		}
	}
	out := hits[:0]
	for _, h := range hits {
		if !drop[h.Rule.ID] {
			out = append(out, h)
		}
	}
	return out
}

// MatchDiagnostics maps check findings onto rules, one hit per finding.
func MatchDiagnostics(rules []*Rule, ds []checks.Diagnostic) []Hit {
	var hits []Hit
	for _, r := range rules {
		for _, d := range ds {
			ok := false
			for _, t := range r.diagRes {
				if (t[0] == nil || t[0].MatchString(d.Tool)) && (t[1] == nil || t[1].MatchString(d.Code)) && (t[2] == nil || t[2].MatchString(d.Message)) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
			vars := map[string]string{"file": d.File, "line": fmt.Sprint(d.Line), "message": d.Message, "tool": d.Tool, "code": d.Code}
			extractAll(r, d.Message+"\n"+d.Detail, vars)
			hits = append(hits, Hit{Rule: r, Key: fmt.Sprintf("%s:%d", d.File, d.Line), Vars: vars, Excerpt: strings.TrimSpace(d.Message + "\n" + d.Detail)})
		}
	}
	return supersede(hits)
}
