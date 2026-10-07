package config

import "testing"

func FuzzParse(f *testing.F) {
	f.Add([]byte("version: 1\nmode: standalone\nterraform:\n  roots:\n    - path: envs/dev\n      env: dev\n"))
	f.Add([]byte("version: 1\ndrift: { schedule: '0 * * * *', notify: webhook }\n"))
	f.Fuzz(func(t *testing.T, b []byte) { Parse(b, "groundwork.yaml") })
}
