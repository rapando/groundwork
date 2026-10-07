package ansible

import "testing"

func FuzzParseEvent(f *testing.F) {
	f.Add([]byte(`{"type":"result","host":"web-1","task":"x","status":"ok","changed":true}`))
	f.Add([]byte(`{"type":"stats","stats":{"web-1":{"ok":1}}}`))
	f.Add([]byte(`{"type":"result","result":null}`))
	f.Fuzz(func(t *testing.T, b []byte) { ParseEvent(b) })
}

func FuzzInventoryAndPatterns(f *testing.F) {
	f.Add([]byte(`{"_meta":{"hostvars":{"a":{}}},"all":{"children":["web"]},"web":{"hosts":["a"]}}`), "web:&prod")
	f.Fuzz(func(t *testing.T, b []byte, pattern string) {
		if inv, err := ParseInventoryList(b); err == nil {
			for _, h := range inv.Hosts {
				inv.GroupsOf(h)
			}
		}
		if ValidPattern(pattern) && len(pattern) > 0 && pattern[0] == '-' {
			t.Fatalf("pattern %q would be read as an option", pattern)
		}
	})
}
