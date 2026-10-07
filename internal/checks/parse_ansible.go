package checks

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ansible-lint's JSON reports a location either as location.positions.begin
// {line,column} or as location.lines.begin (a number, or {line,column}).
type alPos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

func (p *alPos) UnmarshalJSON(b []byte) error {
	var n int
	if json.Unmarshal(b, &n) == nil {
		p.Line = n
		return nil
	}
	type plain alPos
	return json.Unmarshal(b, (*plain)(p))
}

type alIssue struct {
	CheckName   string `json:"check_name"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	URL         string `json:"url"`
	Location    struct {
		Path      string `json:"path"`
		Positions *struct {
			Begin alPos `json:"begin"`
			End   alPos `json:"end"`
		} `json:"positions"`
		Lines *struct {
			Begin alPos `json:"begin"`
			End   alPos `json:"end"`
		} `json:"lines"`
	} `json:"location"`
	Content struct {
		Body string `json:"body"`
	} `json:"content"`
}

func alSeverity(s string) string {
	switch strings.ToLower(s) {
	case "blocker", "critical":
		return SevError
	case "minor", "info":
		return SevInfo
	}
	return SevWarning
}

// ParseAnsibleLint converts `ansible-lint -f json` output.
func ParseAnsibleLint(out []byte, base string) ([]Diagnostic, error) {
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, nil
	}
	var issues []alIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parse ansible-lint output: %w", err)
	}
	var ds []Diagnostic
	for _, i := range issues {
		d := Diagnostic{
			Tool: "ansible-lint", Severity: alSeverity(i.Severity), Code: i.CheckName,
			Message: i.Description, Detail: i.Content.Body, Link: i.URL,
			File: repoPath("", base, i.Location.Path),
		}
		switch {
		case i.Location.Positions != nil:
			d.Line, d.Col = i.Location.Positions.Begin.Line, i.Location.Positions.Begin.Column
			d.EndLine, d.EndCol = i.Location.Positions.End.Line, i.Location.Positions.End.Column
		case i.Location.Lines != nil:
			d.Line, d.Col = i.Location.Lines.Begin.Line, i.Location.Lines.Begin.Column
			d.EndLine = i.Location.Lines.End.Line
		}
		if d.Line > 0 && d.Col == 0 {
			d.Col = 1
		}
		ds = append(ds, d)
	}
	return ds, nil
}

var yamllintRe = regexp.MustCompile(`^(.+?):(\d+):(\d+): \[(error|warning)\] (.*?)(?: \(([\w-]+)\))?$`)

// ParseYamllint converts `yamllint -f parsable` output.
func ParseYamllint(out []byte, base string) []Diagnostic {
	var ds []Diagnostic
	for _, l := range strings.Split(string(out), "\n") {
		m := yamllintRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		line, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		sev := SevWarning
		if m[4] == "error" {
			sev = SevError
		}
		ds = append(ds, Diagnostic{
			Tool: "yamllint", Severity: sev, Code: m[6], Message: m[5],
			File: repoPath("", base, m[1]), Line: line, Col: col,
		})
	}
	return ds
}

var (
	// ansible-core >= 2.19: "[ERROR]: msg\nOrigin: /path/file.yml:6:12"
	synNew = regexp.MustCompile(`(?s)\[ERROR\]: (.+?)\nOrigin: (.+?):(\d+):(\d+)`)
	// older: "ERROR! msg\n\nThe error appears to be in '/path/file.yml': line 6, column 12"
	synOld = regexp.MustCompile(`(?s)ERROR! (.+?)\n.*?The error appears to be in '(.+?)': line (\d+), column (\d+)`)
)

// ParseSyntaxCheck extracts the error from `ansible-playbook --syntax-check`
// output (stdout and stderr combined). file is the playbook that was checked,
// used when the tool gives no location.
func ParseSyntaxCheck(out []byte, root, base, file string) []Diagnostic {
	s := string(out)
	for _, re := range []*regexp.Regexp{synNew, synOld} {
		if m := re.FindStringSubmatch(s); m != nil {
			line, _ := strconv.Atoi(m[3])
			col, _ := strconv.Atoi(m[4])
			return []Diagnostic{{
				Tool: "syntax-check", Severity: SevError, Code: "syntax-check",
				Message: strings.TrimSpace(strings.SplitN(strings.TrimSpace(m[1]), "\nOrigin", 2)[0]),
				File:    repoPath(root, base, m[2]), Line: line, Col: col,
			}}
		}
	}
	// ERROR without a location: report against the playbook.
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if msg, ok := strings.CutPrefix(l, "[ERROR]: "); ok {
			return []Diagnostic{{Tool: "syntax-check", Severity: SevError, Code: "syntax-check", Message: msg, File: path.Clean(file), Line: 1, Col: 1}}
		}
		if msg, ok := strings.CutPrefix(l, "ERROR! "); ok {
			return []Diagnostic{{Tool: "syntax-check", Severity: SevError, Code: "syntax-check", Message: msg, File: path.Clean(file), Line: 1, Col: 1}}
		}
	}
	return nil
}
