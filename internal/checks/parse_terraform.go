package checks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// ParseValidate converts `terraform validate -json` output.
func ParseValidate(out []byte, base string) ([]Diagnostic, error) {
	var v tfjson.ValidateOutput
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("parse validate output: %w", err)
	}
	var ds []Diagnostic
	for _, d := range v.Diagnostics {
		x := Diagnostic{Tool: "validate", Message: d.Summary, Detail: d.Detail, File: base}
		switch d.Severity {
		case tfjson.DiagnosticSeverityWarning:
			x.Severity = SevWarning
		default:
			x.Severity = SevError
		}
		if r := d.Range; r != nil {
			x.File = repoPath("", base, r.Filename)
			x.Line, x.Col = r.Start.Line, r.Start.Column
			x.EndLine, x.EndCol = r.End.Line, r.End.Column
		}
		ds = append(ds, x)
	}
	// terraform reports in map order; show top-to-bottom.
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].File != ds[j].File {
			return ds[i].File < ds[j].File
		}
		return ds[i].Line < ds[j].Line
	})
	return ds, nil
}

// ParseFmtList converts `terraform fmt -check -list=true` output (one
// unformatted file per line).
func ParseFmtList(out []byte, base string) []Diagnostic {
	var ds []Diagnostic
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		f := repoPath("", base, l)
		ds = append(ds, Diagnostic{
			Tool: "fmt", Severity: SevWarning, Code: "fmt", Message: "File is not formatted",
			File: f, Line: 1, Col: 1,
			Fix: &QuickFix{Title: "Format file", Kind: "fmt", File: f},
		})
	}
	return ds
}

type tflintOut struct {
	Issues []struct {
		Rule struct {
			Name     string `json:"name"`
			Severity string `json:"severity"`
			Link     string `json:"link"`
		} `json:"rule"`
		Message string `json:"message"`
		Range   struct {
			Filename string                     `json:"filename"`
			Start    struct{ Line, Column int } `json:"start"`
			End      struct{ Line, Column int } `json:"end"`
		} `json:"range"`
	} `json:"issues"`
	Errors []struct {
		Message  string `json:"message"`
		Severity string `json:"severity"`
		Range    *struct {
			Filename string                     `json:"filename"`
			Start    struct{ Line, Column int } `json:"start"`
		} `json:"range"`
	} `json:"errors"`
}

func tflintSeverity(s string) string {
	switch strings.ToLower(s) {
	case "error":
		return SevError
	case "notice", "info":
		return SevInfo
	}
	return SevWarning
}

// ParseTflint converts `tflint --format=json` output.
func ParseTflint(out []byte, base string) ([]Diagnostic, error) {
	var t tflintOut
	if err := json.Unmarshal(out, &t); err != nil {
		return nil, fmt.Errorf("parse tflint output: %w", err)
	}
	var ds []Diagnostic
	for _, i := range t.Issues {
		ds = append(ds, Diagnostic{
			Tool: "tflint", Severity: tflintSeverity(i.Rule.Severity), Code: i.Rule.Name, Message: i.Message,
			File: repoPath("", base, i.Range.Filename), Link: i.Rule.Link,
			Line: i.Range.Start.Line, Col: i.Range.Start.Column,
			EndLine: i.Range.End.Line, EndCol: i.Range.End.Column,
		})
	}
	for _, e := range t.Errors {
		d := Diagnostic{Tool: "tflint", Severity: SevError, Code: "tflint-error", Message: e.Message, File: base}
		if e.Range != nil {
			d.File, d.Line, d.Col = repoPath("", base, e.Range.Filename), e.Range.Start.Line, e.Range.Start.Column
		}
		ds = append(ds, d)
	}
	return ds, nil
}

type checkovResult struct {
	Results struct {
		Failed []struct {
			CheckID   string `json:"check_id"`
			CheckName string `json:"check_name"`
			FilePath  string `json:"file_path"`
			Lines     []int  `json:"file_line_range"`
			Guideline string `json:"guideline"`
			Resource  string `json:"resource"`
		} `json:"failed_checks"`
	} `json:"results"`
}

// ParseCheckov converts `checkov -o json` output (an object, or an array of
// objects when several frameworks ran).
func ParseCheckov(out []byte, base string) ([]Diagnostic, error) {
	var rs []checkovResult
	trimmed := strings.TrimSpace(string(out))
	switch {
	case strings.HasPrefix(trimmed, "["):
		if err := json.Unmarshal(out, &rs); err != nil {
			return nil, fmt.Errorf("parse checkov output: %w", err)
		}
	default:
		var r checkovResult
		if err := json.Unmarshal(out, &r); err != nil {
			return nil, fmt.Errorf("parse checkov output: %w", err)
		}
		rs = []checkovResult{r}
	}
	var ds []Diagnostic
	for _, r := range rs {
		for _, f := range r.Results.Failed {
			d := Diagnostic{
				Tool: "checkov", Severity: SevWarning, Code: f.CheckID, Message: f.CheckName,
				Detail: f.Resource, File: repoPath("", base, f.FilePath), Link: f.Guideline,
			}
			if len(f.Lines) > 0 {
				d.Line, d.EndLine = f.Lines[0], f.Lines[len(f.Lines)-1]
				d.Col = 1
			}
			ds = append(ds, d)
		}
	}
	return ds, nil
}

var didYouMean = regexp.MustCompile(`Did you mean "([^"]+)"\?`)
var quotedName = regexp.MustCompile(`named "([^"]+)"`)

// FixFromHint builds a rename quick fix when terraform itself suggested one
// ("... Did you mean "triggers_replace"?"). Returns nil if there is no hint.
func FixFromHint(d Diagnostic) *QuickFix {
	if d.Tool != "validate" || d.Line == 0 || d.EndLine != d.Line {
		return nil
	}
	sug := didYouMean.FindStringSubmatch(d.Detail)
	bad := quotedName.FindStringSubmatch(d.Detail)
	if sug == nil || bad == nil || len(bad[1]) != d.EndCol-d.Col {
		return nil
	}
	return &QuickFix{
		Title: fmt.Sprintf("Replace with %s", sug[1]), Kind: "edit",
		Edits: []Edit{{File: d.File, Line: d.Line, Col: d.Col, EndLine: d.EndLine, EndCol: d.EndCol, Old: bad[1], New: sug[1]}},
	}
}
