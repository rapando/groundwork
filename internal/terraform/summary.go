package terraform

import (
	"encoding/json"
	"fmt"
	"sort"

	tfjson "github.com/hashicorp/terraform-json"
)

// Change is one resource-level change in a plan. Values are deliberately not
// included: plans can carry sensitive data, and summaries are stored.
type Change struct {
	Address      string     `json:"address"`
	Type         string     `json:"type"`
	Name         string     `json:"name"`
	Module       string     `json:"module,omitempty"`
	Action       string     `json:"action"` // create | update | replace | delete | read
	ReplacePaths [][]string `json:"replace_paths,omitempty"`
	Reason       string     `json:"reason,omitempty"`
}

type PlanSummary struct {
	Create    int      `json:"create"`
	Update    int      `json:"update"`
	Replace   int      `json:"replace"`
	Delete    int      `json:"delete"`
	Read      int      `json:"read"`
	Changes   []Change `json:"changes"`
	Applyable bool     `json:"applyable"`
	Version   string   `json:"terraform_version,omitempty"`
}

// Total is the number of resources that will be touched.
func (s *PlanSummary) Total() int { return s.Create + s.Update + s.Replace + s.Delete }

// Classify maps terraform's action list to one action. A replacement is
// ["delete","create"] or ["create","delete"] (create-before-destroy).
func Classify(actions []string) string {
	has := func(a string) bool {
		for _, x := range actions {
			if x == a {
				return true
			}
		}
		return false
	}
	switch {
	case has("create") && has("delete"):
		return "replace"
	case has("create"):
		return "create"
	case has("delete"):
		return "delete"
	case has("update"):
		return "update"
	case has("read"):
		return "read"
	}
	return "no-op"
}

// SummarizePlan reads `terraform show -json <planfile>` output.
func SummarizePlan(showJSON []byte) (*PlanSummary, error) {
	var p tfjson.Plan
	if err := json.Unmarshal(showJSON, &p); err != nil {
		return nil, fmt.Errorf("parse plan json: %w", err)
	}
	var flags struct {
		Applyable bool `json:"applyable"`
	}
	_ = json.Unmarshal(showJSON, &flags) // newer terraform only; absent means unknown
	s := &PlanSummary{Changes: []Change{}, Applyable: flags.Applyable, Version: p.TerraformVersion}
	for _, rc := range p.ResourceChanges {
		if rc.Change == nil {
			continue
		}
		acts := make([]string, len(rc.Change.Actions))
		for i, a := range rc.Change.Actions {
			acts[i] = string(a)
		}
		action := Classify(acts)
		switch action {
		case "no-op":
			continue
		case "create":
			s.Create++
		case "update":
			s.Update++
		case "replace":
			s.Replace++
		case "delete":
			s.Delete++
		case "read":
			s.Read++
		}
		ch := Change{Address: rc.Address, Type: rc.Type, Name: rc.Name, Module: rc.ModuleAddress, Action: action, Reason: string(rc.ActionReason)}
		for _, rp := range rc.Change.ReplacePaths {
			if path, ok := rp.([]any); ok {
				var parts []string
				for _, seg := range path {
					parts = append(parts, fmt.Sprint(seg))
				}
				ch.ReplacePaths = append(ch.ReplacePaths, parts)
			}
		}
		s.Changes = append(s.Changes, ch)
	}
	sort.SliceStable(s.Changes, func(i, j int) bool { return s.Changes[i].Address < s.Changes[j].Address })
	return s, nil
}
