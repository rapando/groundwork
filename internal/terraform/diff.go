package terraform

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rapando/groundwork/internal/redact"
)

const (
	Sensitive = "(sensitive value)"
	Unknown   = "(known after apply)"
	maxLeaves = 1000
	maxValue  = 240
)

// AttrDiff is one attribute of one resource, before → after. Values are
// rendered strings; sensitive values are never included.
type AttrDiff struct {
	Path              string `json:"path"`
	Kind              string `json:"kind"` // add | remove | change | same
	Before            string `json:"before,omitempty"`
	After             string `json:"after,omitempty"`
	ForcesReplacement bool   `json:"forces_replacement,omitempty"`
}

type ResourceDiff struct {
	Address        string     `json:"address"`
	Type           string     `json:"type"`
	Action         string     `json:"action"`
	Reason         string     `json:"reason,omitempty"`
	ReplacePaths   []string   `json:"replace_paths,omitempty"`
	Changed        []AttrDiff `json:"changed"`
	Unchanged      []AttrDiff `json:"unchanged"`
	UnchangedCount int        `json:"unchanged_count"`
	Truncated      bool       `json:"truncated,omitempty"`
}

type leaf struct {
	raw       any
	sensitive bool
	unknown   bool
}

var ident = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func joinKey(prefix, k string) string {
	if ident.MatchString(k) {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	return prefix + "[" + strconv.Quote(k) + "]"
}

func joinIdx(prefix string, i int) string { return prefix + "[" + strconv.Itoa(i) + "]" }

// sub returns the child of a sensitivity/unknown marker tree. A marker of
// `true` at a node covers everything beneath it.
func sub(m any, key any) any {
	if b, ok := m.(bool); ok {
		return b
	}
	switch mm := m.(type) {
	case map[string]any:
		if k, ok := key.(string); ok {
			return mm[k]
		}
	case []any:
		if i, ok := key.(int); ok && i < len(mm) {
			return mm[i]
		}
	}
	return nil
}

func isTrue(m any) bool { b, ok := m.(bool); return ok && b }

// flatten walks a value with its sensitivity and unknown markers in parallel.
func flatten(v, sens, unk any, path string, out map[string]leaf) {
	if len(out) >= maxLeaves {
		return
	}
	if isTrue(sens) {
		out[path] = leaf{raw: v, sensitive: true, unknown: isTrue(unk)}
		return
	}
	if isTrue(unk) {
		out[path] = leaf{unknown: true}
		return
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 && !hasKeys(unk) {
			out[path] = leaf{raw: x}
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		if um, ok := unk.(map[string]any); ok { // attributes that only exist after apply
			for k := range um {
				if _, seen := x[k]; !seen {
					keys = append(keys, k)
				}
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			flatten(x[k], sub(sens, k), sub(unk, k), joinKey(path, k), out)
		}
	case []any:
		if len(x) == 0 {
			out[path] = leaf{raw: x}
			return
		}
		for i, e := range x {
			flatten(e, sub(sens, i), sub(unk, i), joinIdx(path, i), out)
		}
	default:
		if path != "" {
			out[path] = leaf{raw: v}
		}
	}
}

func hasKeys(m any) bool { mm, ok := m.(map[string]any); return ok && len(mm) > 0 }

func render(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > maxValue {
		s = s[:maxValue] + "…"
	}
	return s
}

// secretSet collects every value terraform marked sensitive anywhere in the
// plan, plus sensitive variables. Terraform does not always propagate the
// marking (terraform_data copies input → output unmarked), so these values
// are masked wherever they appear, not only where they are marked.
type secretSet struct {
	exact map[string]bool
	red   *redact.Redactor
}

func (s *secretSet) add(v any) {
	switch x := v.(type) {
	case string:
		if x != "" {
			s.exact[x] = true
			s.red.AddSecret(x)
		}
	case float64, bool:
		s.exact[render(x)] = true
	case map[string]any:
		for _, e := range x {
			s.add(e)
		}
	case []any:
		for _, e := range x {
			s.add(e)
		}
	}
}

func collectSensitive(v, sens any, s *secretSet) {
	if isTrue(sens) {
		s.add(v)
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			collectSensitive(e, sub(sens, k), s)
		}
	case []any:
		for i, e := range x {
			collectSensitive(e, sub(sens, i), s)
		}
	}
}

// mask renders one side of a leaf, hiding anything secret.
func (s *secretSet) mask(l leaf, present bool) string {
	switch {
	case !present:
		return ""
	case l.sensitive:
		return Sensitive
	case l.unknown:
		return Unknown
	}
	if str, ok := l.raw.(string); ok && s.exact[str] {
		return Sensitive
	}
	r := render(l.raw)
	if s.exact[r] {
		return Sensitive
	}
	return s.red.Line(r) // embedded secrets and well-known token formats
}

type rawPlan struct {
	Variables map[string]struct {
		Value any `json:"value"`
	} `json:"variables"`
	Configuration struct {
		RootModule struct {
			Variables map[string]struct {
				Sensitive bool `json:"sensitive"`
			} `json:"variables"`
		} `json:"root_module"`
	} `json:"configuration"`
	ResourceChanges []rawChange `json:"resource_changes"`
	ResourceDrift   []rawChange `json:"resource_drift"`
}

type rawChange struct {
	Address      string `json:"address"`
	Type         string `json:"type"`
	ActionReason string `json:"action_reason"`
	Change       struct {
		Actions         []string `json:"actions"`
		Before          any      `json:"before"`
		After           any      `json:"after"`
		AfterUnknown    any      `json:"after_unknown"`
		BeforeSensitive any      `json:"before_sensitive"`
		AfterSensitive  any      `json:"after_sensitive"`
		ReplacePaths    [][]any  `json:"replace_paths"`
	} `json:"change"`
}

func pathString(p []any) string {
	s := ""
	for _, seg := range p {
		switch v := seg.(type) {
		case string:
			s = joinKey(s, v)
		case float64:
			s = joinIdx(s, int(v))
		}
	}
	return s
}

// ComputeDiffs turns `terraform show -json <plan>` into masked per-resource
// attribute diffs. The output never contains sensitive values.
func ComputeDiffs(showJSON []byte) (map[string]*ResourceDiff, error) {
	p, secrets, err := parsePlan(showJSON)
	if err != nil {
		return nil, err
	}
	return diffChanges(p.ResourceChanges, secrets), nil
}

// ComputeDrift returns masked diffs for `resource_drift` in a refresh-only
// plan: before is what Terraform's state recorded, after is what exists now.
func ComputeDrift(showJSON []byte) (map[string]*ResourceDiff, error) {
	p, secrets, err := parsePlan(showJSON)
	if err != nil {
		return nil, err
	}
	return diffChanges(p.ResourceDrift, secrets), nil
}

func parsePlan(showJSON []byte) (*rawPlan, *secretSet, error) {
	var p rawPlan
	if err := json.Unmarshal(showJSON, &p); err != nil {
		return nil, nil, fmt.Errorf("parse plan json: %w", err)
	}
	secrets := &secretSet{exact: map[string]bool{}, red: redact.New()}
	for name, v := range p.Variables {
		if p.Configuration.RootModule.Variables[name].Sensitive {
			secrets.add(v.Value)
		}
	}
	for _, rc := range append(append([]rawChange(nil), p.ResourceChanges...), p.ResourceDrift...) {
		collectSensitive(rc.Change.Before, rc.Change.BeforeSensitive, secrets)
		collectSensitive(rc.Change.After, rc.Change.AfterSensitive, secrets)
	}
	return &p, secrets, nil
}

func diffChanges(changes []rawChange, secrets *secretSet) map[string]*ResourceDiff {
	out := map[string]*ResourceDiff{}
	for _, rc := range changes {
		action := Classify(rc.Change.Actions)
		if action == "no-op" {
			continue
		}
		d := &ResourceDiff{Address: rc.Address, Type: rc.Type, Action: action, Reason: rc.ActionReason, Changed: []AttrDiff{}, Unchanged: []AttrDiff{}}
		for _, rp := range rc.Change.ReplacePaths {
			d.ReplacePaths = append(d.ReplacePaths, pathString(rp))
		}
		before, after := map[string]leaf{}, map[string]leaf{}
		flatten(rc.Change.Before, rc.Change.BeforeSensitive, nil, "", before)
		flatten(rc.Change.After, rc.Change.AfterSensitive, rc.Change.AfterUnknown, "", after)
		d.Truncated = len(before) >= maxLeaves || len(after) >= maxLeaves

		paths := map[string]bool{}
		for k := range before {
			paths[k] = true
		}
		for k := range after {
			paths[k] = true
		}
		keys := make([]string, 0, len(paths))
		for k := range paths {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b, hasB := before[k]
			a, hasA := after[k]
			ad := AttrDiff{Path: k, Before: secrets.mask(b, hasB), After: secrets.mask(a, hasA)}
			switch {
			case action == "delete":
				ad.Kind, ad.After = "remove", ""
			case !hasB || b.raw == nil && !b.sensitive:
				if !hasA || a.raw == nil && !a.unknown && !a.sensitive {
					continue // null → null: nothing to show
				}
				ad.Kind = "add"
			case !hasA:
				ad.Kind = "remove"
			case a.unknown || render(a.raw) != render(b.raw) || a.sensitive != b.sensitive:
				ad.Kind = "change"
			default:
				ad.Kind = "same"
			}
			for _, rp := range d.ReplacePaths {
				if k == rp || strings.HasPrefix(k, rp+".") || strings.HasPrefix(k, rp+"[") {
					ad.ForcesReplacement = true
				}
			}
			if ad.Kind == "same" {
				d.UnchangedCount++
				if len(d.Unchanged) < 200 {
					d.Unchanged = append(d.Unchanged, ad)
				}
				continue
			}
			d.Changed = append(d.Changed, ad)
		}
		// replacement causes first, as in terraform's own output
		sort.SliceStable(d.Changed, func(i, j int) bool { return d.Changed[i].ForcesReplacement && !d.Changed[j].ForcesReplacement })
		out[rc.Address] = d
	}
	return out
}
