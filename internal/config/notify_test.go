package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNotifyDefaults(t *testing.T) {
	var nilCfg *Config
	s := nilCfg.NotifySettings()
	if !slices.Equal(s.Failed, []string{"console", "desktop"}) || !slices.Equal(s.Approval, []string{"console", "desktop"}) ||
		!slices.Equal(s.Success, []string{"console"}) || !slices.Equal(s.Drift, []string{"console", "desktop"}) ||
		s.QuietUnder != "30s" || s.Quiet() != 30*time.Second || len(s.Envs) != 0 || s.Webhook != "" || nilCfg.NotifyConfigured() {
		t.Fatalf("%+v", s)
	}
	// legacy drift settings map onto the defaults
	cases := map[string][]string{"desktop": {"console", "desktop"}, "webhook": {"console", "webhook"}, "none": {"console"}, "": {"console", "desktop"}}
	for notify, want := range cases {
		c := &Config{Drift: Drift{Notify: notify, Webhook: "https://hooks.example.com/x"}}
		s := c.NotifySettings()
		if !slices.Equal(s.Drift, want) {
			t.Errorf("drift.notify %q: %v", notify, s.Drift)
		}
		if s.Webhook != "https://hooks.example.com/x" || !slices.Equal(s.Failed, []string{"console", "desktop", "webhook"}) {
			t.Errorf("drift.webhook not carried over: %+v", s)
		}
	}
}

func TestNotifyBlock(t *testing.T) {
	src := `version: 1
mode: standalone
drift: { notify: webhook, webhook: "https://old.example/x" }
notify:
  failed: [console, webhook, console]
  success: []
  quiet_under: 1m
  envs: [prod]
`
	c, err := Parse([]byte(src), FileName)
	if err != nil {
		t.Fatal(err)
	}
	s := c.NotifySettings()
	if !c.NotifyConfigured() || !slices.Equal(s.Failed, []string{"console", "webhook"}) || len(s.Success) != 0 || s.Success == nil ||
		!slices.Equal(s.Approval, []string{"console", "desktop"}) || !slices.Equal(s.Drift, []string{"console", "webhook"}) ||
		s.Quiet() != time.Minute || s.Webhook != "https://old.example/x" {
		t.Fatalf("%+v", s)
	}
	if !s.EnvAllowed("prod") || s.EnvAllowed("dev") {
		t.Fatal("envs filter")
	}
	if !slices.Equal(s.Channels("succeeded"), s.Success) || !slices.Equal(s.Channels("approval"), s.Approval) ||
		!slices.Equal(s.Channels("drift"), s.Drift) || s.Channels("cancelled") != nil {
		t.Fatal("channels")
	}
	if (NotifySettings{QuietUnder: "bogus"}).Quiet() != DefaultQuietUnder {
		t.Fatal("bad quiet_under should fall back")
	}
	b := (NotifySettings{}).Block()
	if b.Failed == nil || b.Drift == nil {
		t.Fatal("Block must make every list explicit")
	}
}

func TestNotifyValidation(t *testing.T) {
	bad := map[string]string{
		"notify: { failed: [pager] }":                         "unknown channel",
		"notify: { quiet_under: soon }":                       "notify.quiet_under",
		"notify: { quiet_under: -1s }":                        "notify.quiet_under",
		"notify: { envs: ['-chdir=/'] }":                      "notify.envs[0]",
		"notify: { webhook: 'ftp://h/x' }":                    "http",
		"notify: { webhook: 'https://u:p@h/x' }":              "credentials",
		"notify: { drift: [webhook] }":                        "", // the URL may come from the settings for every project
		"notify: { failed: [console], extra: 1 }":             "extra",
		"notify: { failed: [webhook], webhook: 'https://h' }": "",
	}
	for block, want := range bad {
		_, err := Parse([]byte("version: 1\nmode: standalone\n"+block+"\n"), FileName)
		if want == "" {
			if err != nil {
				t.Errorf("%s: %v", block, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", block, want, err)
		}
	}
	// drift.webhook satisfies a webhook channel
	if _, err := Parse([]byte("version: 1\nmode: standalone\ndrift: { webhook: 'https://h/x' }\nnotify: { failed: [webhook] }\n"), FileName); err != nil {
		t.Fatal(err)
	}
	c := &Config{}
	if p := c.ValidateNotify(NotifySettings{Failed: []string{"sms"}, QuietUnder: "x"}); len(p) != 2 || !strings.HasPrefix(p[0], "notify.failed[0]: ") {
		t.Fatalf("%v", p)
	}
	if p := c.ValidateNotify(c.NotifySettings()); len(p) != 0 {
		t.Fatalf("defaults must validate: %v", p)
	}
}

func TestSetNotifyBlock(t *testing.T) {
	src := `# groundwork configuration. Edit freely.
---
version: 1
mode: standalone # how the repo is laid out
notify:
  failed: [console,
    desktop
  ]
  # a comment inside the block
  success: []

# drift settings
drift:
  schedule: "0 7 * * 1-5" # weekdays
`
	n := Notify{Failed: []string{"console", "webhook"}, Approval: []string{}, Success: []string{"console"}, Drift: []string{"console"},
		QuietUnder: "1m", Envs: []string{"prod", "1.0"}, Webhook: "https://hooks.example.com/x?a=1#frag"}
	out, err := SetNotifyBlock([]byte(src), n)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, keep := range []string{"# groundwork configuration. Edit freely.\n---\n", "mode: standalone # how the repo is laid out\n",
		"\n# drift settings\ndrift:\n  schedule: \"0 7 * * 1-5\" # weekdays\n"} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %q:\n%s", keep, s)
		}
	}
	if strings.Contains(s, "inside the block") || strings.Count(s, "notify:") != 1 {
		t.Errorf("old block kept:\n%s", s)
	}
	c, err := Parse(out, FileName)
	if err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	got := c.NotifySettings()
	if !slices.Equal(got.Failed, n.Failed) || len(got.Approval) != 0 || got.Webhook != n.Webhook || !slices.Equal(got.Envs, n.Envs) || got.QuietUnder != "1m" {
		t.Fatalf("%+v\n%s", got, s)
	}

	// no block yet: appended, the rest untouched
	plain := "version: 1\nmode: standalone"
	out, err = SetNotifyBlock([]byte(plain), Notify{Failed: []string{"console"}})
	if err != nil || !strings.HasPrefix(string(out), plain+"\nnotify:\n  failed: [console]\n") {
		t.Fatalf("%v\n%s", err, out)
	}
	// a list left out isn't written, so it keeps following the settings below it
	if c, err := Parse(out, FileName); err != nil || c.Notify.Drift != nil || strings.Contains(string(out), "drift") {
		t.Fatalf("%v %+v", err, c)
	}
	// nothing overridden: the block goes, the rest stays
	out, err = SetNotifyBlock([]byte("version: 1\nnotify:\n  failed: [console]\n# tail\nmode: standalone\n"), Notify{})
	if err != nil || string(out) != "version: 1\n# tail\nmode: standalone\n" {
		t.Fatalf("%v %q", err, out)
	}
	out, _ = SetNotifyBlock([]byte("version: 1\nmode: standalone\n"), Notify{})
	if string(out) != "version: 1\nmode: standalone\n" {
		t.Fatalf("%q", out)
	}
	// a block on one line
	out, _ = SetNotifyBlock([]byte("version: 1\nnotify: {failed: [desktop]}\nmode: standalone\n"), Notify{Failed: []string{"console"}})
	if !strings.HasSuffix(string(out), "mode: standalone\n") || strings.Contains(string(out), "desktop") {
		t.Fatalf("%s", out)
	}
	for _, bad := range []string{"{version: 1}", "- a\n", ": :"} {
		if _, err := SetNotifyBlock([]byte(bad), Notify{}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The settings for every project sit under the project's: each key comes
// from the project's block, its legacy drift keys, the base, the default.
func TestNotifySettingsOver(t *testing.T) {
	base := &Notify{Failed: []string{"console", "webhook"}, Success: []string{"console", "desktop"}, Drift: []string{"console"},
		QuietUnder: "2m", Envs: []string{"prod"}, Webhook: "https://hooks.example.com/all"}
	var nilCfg *Config
	if s := nilCfg.NotifySettingsOver(base); !slices.Equal(s.Failed, base.Failed) || !slices.Equal(s.Approval, []string{"console", "desktop"}) ||
		!slices.Equal(s.Success, base.Success) || !slices.Equal(s.Drift, base.Drift) || s.QuietUnder != "2m" || !slices.Equal(s.Envs, base.Envs) || s.Webhook != base.Webhook {
		t.Fatalf("base alone: %+v", s)
	}
	if s := ResolveNotify(nil); !slices.Equal(s.Failed, []string{"console", "desktop"}) || s.Webhook != "" {
		t.Fatalf("no base is the defaults: %+v", s)
	}
	// the base webhook turns on the default webhook for failures
	if s := ResolveNotify(&Notify{Webhook: "https://h/x"}); !slices.Equal(s.Failed, []string{"console", "desktop", "webhook"}) {
		t.Fatalf("%+v", s)
	}
	c := &Config{Notify: &Notify{Success: []string{}, Envs: []string{}, QuietUnder: "0s"}}
	s := c.NotifySettingsOver(base)
	if len(s.Success) != 0 || len(s.Envs) != 0 || s.QuietUnder != "0s" || !slices.Equal(s.Failed, base.Failed) || s.Webhook != base.Webhook {
		t.Fatalf("project overrides: %+v", s)
	}
	// legacy drift keys beat the base; unset drift.notify follows it
	c = &Config{Drift: Drift{Notify: "webhook", Webhook: "https://old.example/x"}}
	if s := c.NotifySettingsOver(base); !slices.Equal(s.Drift, []string{"console", "webhook"}) || s.Webhook != "https://old.example/x" {
		t.Fatalf("legacy: %+v", s)
	}
	if s := (&Config{}).NotifySettingsOver(base); !slices.Equal(s.Drift, base.Drift) {
		t.Fatalf("unset drift.notify: %+v", s)
	}
	if o := (&Config{}).NotifyOverrides(); !o.Empty() || o.Count() != 0 {
		t.Fatal("no block, no overrides")
	}
	if n := (Notify{Failed: []string{}, Webhook: "x"}); n.Empty() || n.Count() != 2 {
		t.Fatal("count")
	}
	// a project's webhook channel is satisfied by the base URL
	if p := (&Config{}).ValidateNotifyOverrides(Notify{Drift: []string{"webhook"}}, base); len(p) != 0 {
		t.Fatalf("%v", p)
	}
	if p := (&Config{}).ValidateNotifyOverrides(Notify{Drift: []string{"webhook"}}, nil); len(p) != 1 || !strings.Contains(p[0], "webhook URL required") {
		t.Fatalf("%v", p)
	}
}

func TestNotifyFile(t *testing.T) {
	b, err := RenderNotifyFile(ResolveNotify(nil).Block())
	if err != nil || !strings.HasPrefix(string(b), "# Notification settings for every project") {
		t.Fatalf("%v\n%s", err, b)
	}
	n, err := ParseNotifyFile(b)
	if err != nil || !reflect.DeepEqual(ResolveNotify(n), ResolveNotify(nil)) {
		t.Fatalf("%v %+v", err, n)
	}
	if n, err := ParseNotifyFile([]byte("# nothing yet\n")); err != nil || n != nil {
		t.Fatalf("empty: %v %+v", err, n)
	}
	for _, bad := range []string{"notify: { failed: [sms] }", "notify: { extra: 1 }", "other: 1", "notify: { failed: [webhook] }", "- a"} {
		if _, err := ParseNotifyFile([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
