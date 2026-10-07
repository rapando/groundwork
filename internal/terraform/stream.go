// Package terraform adapts the terraform/tofu CLI: command lines, the
// machine-readable (-json) output stream, and plan summaries.
package terraform

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResEvent is a per-resource progress update from apply.
type ResEvent struct {
	Address string `json:"address"`
	Action  string `json:"action"`            // create | update | delete | replace | read
	State   string `json:"state"`             // running | done | failed
	Elapsed int    `json:"elapsed,omitempty"` // seconds, for "still creating"
}

type Diag struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	Address  string `json:"address,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// ChangeCounts is terraform's own tally from a change_summary event.
type ChangeCounts struct {
	Add       int    `json:"add"`
	Change    int    `json:"change"`
	Remove    int    `json:"remove"`
	Operation string `json:"operation"` // plan | apply | destroy
}

type Event struct {
	Type    string
	Level   string // info | warn | error
	Message string
	Res     *ResEvent
	Diag    *Diag
	Changes *ChangeCounts
	Outputs []string // output names, from an "outputs" event
}

type rawEvent struct {
	Level   string                     `json:"@level"`
	Message string                     `json:"@message"`
	Type    string                     `json:"type"`
	Changes *ChangeCounts              `json:"changes"`
	Outputs map[string]json.RawMessage `json:"outputs"`
	Hook    *struct {
		Resource struct {
			Addr string `json:"addr"`
		} `json:"resource"`
		Action  string `json:"action"`
		Elapsed int    `json:"elapsed_seconds"`
	} `json:"hook"`
	Diagnostic *struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
		Address  string `json:"address"`
		Range    *struct {
			Filename string             `json:"filename"`
			Start    struct{ Line int } `json:"start"`
		} `json:"range"`
	} `json:"diagnostic"`
}

// ParseLine decodes one line of `-json` output. ok is false when the line is
// not JSON (plain stderr, a crash message, ...); callers then treat it as text.
func ParseLine(b []byte) (Event, bool) {
	var r rawEvent
	if len(b) == 0 || b[0] != '{' || json.Unmarshal(b, &r) != nil || r.Type == "" {
		return Event{}, false
	}
	e := Event{Type: r.Type, Level: normLevel(r.Level), Message: r.Message}
	if r.Type == "change_summary" {
		e.Changes = r.Changes
	}
	if r.Type == "outputs" {
		for k := range r.Outputs {
			e.Outputs = append(e.Outputs, k)
		}
	}
	if r.Hook != nil {
		res := &ResEvent{Address: r.Hook.Resource.Addr, Action: r.Hook.Action, Elapsed: r.Hook.Elapsed}
		switch r.Type {
		case "apply_start", "apply_progress", "refresh_start":
			res.State = "running"
		case "apply_complete":
			res.State = "done"
		case "apply_errored":
			res.State = "failed"
			e.Level = "error"
		}
		if res.State != "" && strings.HasPrefix(r.Type, "apply_") {
			e.Res = res
		}
	}
	if d := r.Diagnostic; d != nil {
		e.Diag = &Diag{Severity: d.Severity, Summary: d.Summary, Detail: d.Detail, Address: d.Address}
		if d.Range != nil {
			e.Diag.File, e.Diag.Line = d.Range.Filename, d.Range.Start.Line
		}
		e.Level = "error"
		if d.Severity == "warning" {
			e.Level = "warn"
		}
		e.Message = d.Summary
	}
	return e, true
}

func normLevel(l string) string {
	switch strings.ToLower(l) {
	case "warn", "warning":
		return "warn"
	case "error":
		return "error"
	case "debug", "trace":
		return "debug"
	}
	return "info"
}

// Text is the human-readable log line(s) for an event.
func (e Event) Text() string {
	if e.Diag != nil {
		d := e.Diag
		head := "Error: " + d.Summary
		if d.Severity == "warning" {
			head = "Warning: " + d.Summary
		}
		var b strings.Builder
		b.WriteString(head)
		if d.File != "" {
			fmt.Fprintf(&b, "\n  on %s line %d", d.File, d.Line)
			if d.Address != "" {
				fmt.Fprintf(&b, ", in %s", d.Address)
			}
		}
		if d.Detail != "" {
			b.WriteString("\n  " + strings.ReplaceAll(strings.TrimSpace(d.Detail), "\n", "\n  "))
		}
		return b.String()
	}
	return e.Message
}
