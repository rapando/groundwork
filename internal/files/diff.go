package files

import (
	"fmt"
	"strings"
)

// UnifiedDiff renders a unified diff of two versions of a file. It finds the
// common prefix and suffix and shows the middle as one hunk, which is exact
// for the single-region edits groundwork makes.
func UnifiedDiff(name, a, b string) string {
	if a == b {
		return ""
	}
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	const ctx = 3
	start := max(0, pre-ctx)
	aEnd, bEnd := len(al)-suf, len(bl)-suf
	aStop, bStop := min(len(al), aEnd+ctx), min(len(bl), bEnd+ctx)
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", name, name)
	fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", start+1, aStop-start, start+1, bStop-start)
	for i := start; i < pre; i++ {
		sb.WriteString(" " + al[i] + "\n")
	}
	for i := pre; i < aEnd; i++ {
		sb.WriteString("-" + al[i] + "\n")
	}
	for i := pre; i < bEnd; i++ {
		sb.WriteString("+" + bl[i] + "\n")
	}
	for i := aEnd; i < aStop; i++ {
		sb.WriteString(" " + al[i] + "\n")
	}
	return sb.String()
}
