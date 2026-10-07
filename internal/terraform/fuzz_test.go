package terraform

import (
	"bufio"
	"os"
	"testing"
)

func FuzzParseLine(f *testing.F) {
	for _, name := range []string{"plan.jsonl", "apply.jsonl", "plan_error.jsonl", "plan_locked.jsonl", "drift_plan.jsonl"} {
		fh, err := os.Open("../../testdata/terraform/" + name)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			f.Add(append([]byte(nil), sc.Bytes()...))
		}
		fh.Close()
	}
	f.Add([]byte(`{"type":"diagnostic","diagnostic":null}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if ev, ok := ParseLine(b); ok {
			_ = ev.Text()
		}
	})
}
