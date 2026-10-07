// Package checks runs fmt/validate/lint tools and normalises their output.
package checks

// Edit replaces Old with New at a source range. Old is verified against the
// file before the edit is applied, so a stale fix can never corrupt a buffer.
type Edit struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"end_line"`
	EndCol  int    `json:"end_col"`
	Old     string `json:"old"`
	New     string `json:"new"`
}

// QuickFix is a machine-applicable fix. Kind "edit" carries Edits; kind "fmt"
// is applied by running the formatter on File.
type QuickFix struct {
	Title string `json:"title"`
	Kind  string `json:"kind"` // edit | fmt
	Edits []Edit `json:"edits,omitempty"`
	File  string `json:"file,omitempty"`
}

// Diagnostic is the normalised finding every tool maps onto.
type Diagnostic struct {
	ID       int64     `json:"id"`
	Unit     string    `json:"unit"`
	Tool     string    `json:"tool"`
	Severity string    `json:"severity"` // error | warning | info
	Code     string    `json:"code,omitempty"`
	Message  string    `json:"message"`
	Detail   string    `json:"detail,omitempty"`
	File     string    `json:"file"` // repo-relative; a directory for unit-level findings
	Line     int       `json:"line,omitempty"`
	Col      int       `json:"col,omitempty"`
	EndLine  int       `json:"end_line,omitempty"`
	EndCol   int       `json:"end_col,omitempty"`
	Link     string    `json:"link,omitempty"`
	Fix      *QuickFix `json:"fix,omitempty"`
	RuleRef  string    `json:"rule_ref,omitempty"` // troubleshooting rule id (M8)
}

const (
	SevError   = "error"
	SevWarning = "warning"
	SevInfo    = "info"
)
