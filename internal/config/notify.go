package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Notification channels.
const (
	ChannelConsole = "console" // a toast in the console
	ChannelDesktop = "desktop"
	ChannelWebhook = "webhook"
)

// KnownChannels are the values a notify list may hold.
var KnownChannels = []string{ChannelConsole, ChannelDesktop, ChannelWebhook}

// DefaultQuietUnder: successes shorter than this get no toast, desktop
// notification or webhook (they are still recorded).
const DefaultQuietUnder = 30 * time.Second

// Notify is the `notify:` block: which channels each kind of event reaches.
// A list left out takes its default; an empty list ([]) means "record only".
// The service keeps one for every project (notify.yaml in its data
// directory); a project's block in groundwork.yaml overrides it key by key.
// As JSON it is a project's overrides: null (or "") means "not overridden".
type Notify struct {
	Failed     []string `yaml:"failed,omitempty" json:"failed"`
	Approval   []string `yaml:"approval,omitempty" json:"approval"`
	Success    []string `yaml:"success,omitempty" json:"success"`
	Drift      []string `yaml:"drift,omitempty" json:"drift"`
	QuietUnder string   `yaml:"quiet_under,omitempty" json:"quiet_under"`
	Envs       []string `yaml:"envs,omitempty" json:"envs"`
	Webhook    string   `yaml:"webhook,omitempty" json:"webhook"`
}

// Empty reports whether the block sets nothing.
func (n Notify) Empty() bool {
	return n.Failed == nil && n.Approval == nil && n.Success == nil && n.Drift == nil && n.QuietUnder == "" && n.Envs == nil && n.Webhook == ""
}

// Count is how many keys the block sets.
func (n Notify) Count() int {
	c := 0
	for _, l := range [][]string{n.Failed, n.Approval, n.Success, n.Drift, n.Envs} {
		if l != nil {
			c++
		}
	}
	for _, v := range []string{n.QuietUnder, n.Webhook} {
		if v != "" {
			c++
		}
	}
	return c
}

// NotifySettings are the effective notification settings: the `notify:`
// block with defaults filled in (and the legacy drift.notify/drift.webhook
// mapped onto it). It is also the JSON the console reads and writes.
type NotifySettings struct {
	Failed     []string `json:"failed"`
	Approval   []string `json:"approval"`
	Success    []string `json:"success"`
	Drift      []string `json:"drift"`
	QuietUnder string   `json:"quiet_under"`
	Envs       []string `json:"envs"`
	Webhook    string   `json:"webhook"`
}

// NotifySettings resolves the effective settings from the project's config
// alone. A nil config gives the defaults with no webhook.
func (c *Config) NotifySettings() NotifySettings { return c.NotifySettingsOver(nil) }

// ResolveNotify is the effective settings of the block for every project
// (nil: the defaults).
func ResolveNotify(base *Notify) NotifySettings { return (*Config)(nil).NotifySettingsOver(base) }

// NotifySettingsOver resolves the effective settings with base (the
// settings for every project, nil for none) under the project's own. Each
// key comes from the first of: the project's notify: block, its legacy
// drift.notify/drift.webhook, base, the default.
func (c *Config) NotifySettingsOver(base *Notify) NotifySettings {
	var n, b Notify
	var d Drift
	if c != nil {
		d = c.Drift
		if c.Notify != nil {
			n = *c.Notify
		}
	}
	if base != nil {
		b = *base
	}
	s := NotifySettings{Webhook: first(n.Webhook, d.Webhook, b.Webhook), QuietUnder: first(n.QuietUnder, b.QuietUnder, DefaultQuietUnder.String())}
	s.Envs = cleanList(orDefault(n.Envs, b.Envs))
	failed := []string{ChannelConsole, ChannelDesktop}
	if s.Webhook != "" {
		failed = append(failed, ChannelWebhook)
	}
	drift := orDefault(b.Drift, []string{ChannelConsole, ChannelDesktop})
	switch d.Notify {
	case "webhook":
		drift = []string{ChannelConsole, ChannelWebhook}
	case "none":
		drift = []string{ChannelConsole}
	case "desktop":
		drift = []string{ChannelConsole, ChannelDesktop}
	}
	s.Failed = orDefault(n.Failed, orDefault(b.Failed, failed))
	s.Approval = orDefault(n.Approval, orDefault(b.Approval, []string{ChannelConsole, ChannelDesktop}))
	s.Success = orDefault(n.Success, orDefault(b.Success, []string{ChannelConsole}))
	s.Drift = orDefault(n.Drift, drift)
	return s
}

// NotifyOverrides is the project's notify: block (empty when it has none).
func (c *Config) NotifyOverrides() Notify {
	if c == nil || c.Notify == nil {
		return Notify{}
	}
	return *c.Notify
}

func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// Configured reports whether groundwork.yaml has a `notify:` block.
func (c *Config) NotifyConfigured() bool { return c != nil && c.Notify != nil }

// Quiet is quiet_under as a duration (the default if it doesn't parse).
func (s NotifySettings) Quiet() time.Duration {
	if d, err := time.ParseDuration(s.QuietUnder); err == nil && d >= 0 {
		return d
	}
	return DefaultQuietUnder
}

// EnvAllowed reports whether desktop and webhook delivery covers env.
func (s NotifySettings) EnvAllowed(env string) bool {
	return len(s.Envs) == 0 || slices.Contains(s.Envs, env)
}

// Channels returns the channels for an event: failed, approval, succeeded
// (or success) and drift. Anything else (cancelled) reaches no channel.
func (s NotifySettings) Channels(event string) []string {
	switch event {
	case "failed":
		return s.Failed
	case "approval":
		return s.Approval
	case "succeeded", "success":
		return s.Success
	case "drift":
		return s.Drift
	}
	return nil
}

// Block turns effective settings back into a `notify:` block (every list
// explicit, so what was saved is what applies).
func (s NotifySettings) Block() Notify {
	return Notify{Failed: nonNil(s.Failed), Approval: nonNil(s.Approval), Success: nonNil(s.Success), Drift: nonNil(s.Drift),
		QuietUnder: s.QuietUnder, Envs: s.Envs, Webhook: s.Webhook}
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func orDefault(l, def []string) []string {
	if l == nil {
		return def
	}
	return cleanList(l)
}

// cleanList drops duplicates and keeps order; never nil.
func cleanList(l []string) []string {
	out := []string{}
	for _, v := range l {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

type notifyProblem struct {
	msg  string
	keys []any
}

// problems validates the block. With requireURL, listing the webhook channel
// needs a URL here or in fallback; groundwork.yaml skips that check, since
// the URL may come from the settings for every project.
func (n *Notify) problems(fallback string, requireURL bool) []notifyProblem {
	if n == nil {
		return nil
	}
	var out []notifyProblem
	add := func(msg string, keys ...any) { out = append(out, notifyProblem{msg, keys}) }
	wantsWebhook := false
	for _, l := range []struct {
		key string
		chs []string
	}{{"failed", n.Failed}, {"approval", n.Approval}, {"success", n.Success}, {"drift", n.Drift}} {
		for i, ch := range l.chs {
			if !slices.Contains(KnownChannels, ch) {
				add(fmt.Sprintf("unknown channel %q (known: %s)", ch, strings.Join(KnownChannels, ", ")), l.key, i)
			}
			wantsWebhook = wantsWebhook || ch == ChannelWebhook
		}
	}
	if n.QuietUnder != "" {
		if d, err := time.ParseDuration(n.QuietUnder); err != nil || d < 0 {
			add("must be a duration such as 30s or 2m", "quiet_under")
		}
	}
	for i, e := range n.Envs {
		if !ValidEnvName(e) {
			add("environment names may contain letters, digits, '_', '.' and '-'", "envs", i)
		}
	}
	if n.Webhook != "" {
		if msg := checkWebhook(n.Webhook); msg != "" {
			add(msg, "webhook")
		}
	} else if wantsWebhook && requireURL && fallback == "" {
		add("webhook URL required when a webhook channel is listed", "webhook")
	}
	return out
}

// ValidateNotify checks settings submitted from the console, with the
// config's legacy drift.webhook as the fallback URL. It returns the problems
// as user-facing lines.
func (c *Config) ValidateNotify(s NotifySettings) []string {
	return c.ValidateNotifyOverrides(s.Block(), nil)
}

// ValidateNotifyOverrides checks a project's overrides submitted from the
// console: a webhook channel needs a URL in the overrides, drift.webhook or
// base (the settings for every project).
func (c *Config) ValidateNotifyOverrides(n Notify, base *Notify) []string {
	var fb string
	if c != nil {
		fb = c.Drift.Webhook
	}
	if fb == "" && base != nil {
		fb = base.Webhook
	}
	return ValidateNotifyBlock(n, fb)
}

// ValidateNotifyBlock checks a block on its own (fallback: a URL that
// satisfies a webhook channel). It returns the problems as user-facing lines.
func ValidateNotifyBlock(n Notify, fallback string) []string {
	var out []string
	for _, p := range n.problems(fallback, true) {
		out = append(out, pathString(append([]any{"notify"}, p.keys...))+": "+p.msg)
	}
	return out
}

// ParseNotifyFile reads the settings for every project: a file holding a
// top-level notify: block, as in groundwork.yaml. An empty file is no block.
func ParseNotifyFile(b []byte) (*Notify, error) {
	var f struct {
		Notify *Notify `yaml:"notify"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if probs := ValidateNotifyBlock(f.Notify.orEmpty(), ""); len(probs) > 0 {
		return nil, errors.New(strings.Join(probs, "; "))
	}
	return f.Notify, nil
}

func (n *Notify) orEmpty() Notify {
	if n == nil {
		return Notify{}
	}
	return *n
}

// RenderNotifyFile is the content of the file for every project's settings.
func RenderNotifyFile(n Notify) ([]byte, error) {
	block, err := renderNotify(n)
	if err != nil {
		return nil, err
	}
	return []byte("# Notification settings for every project, written by groundwork.\n" +
		"# A project's notify: block in groundwork.yaml overrides them key by key.\n" + block), nil
}

// SetNotifyBlock returns groundwork.yaml content with its top-level
// `notify:` block replaced by n (or added after the last top-level key).
// Every other line, comments included, is kept as it was.
// An empty n removes the block.
func SetNotifyBlock(src []byte, n Notify) ([]byte, error) {
	block := ""
	if !n.Empty() {
		var err error
		if block, err = renderNotify(n); err != nil {
			return nil, err
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode || doc.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, errors.New("groundwork.yaml must be a mapping of settings to add a notify block")
	}
	lines := strings.SplitAfter(string(src), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		k := top.Content[i]
		if k.Value != "notify" {
			continue
		}
		if k.Column != 1 {
			return nil, errors.New("the notify block isn't at the start of a line")
		}
		start := k.Line - 1
		end := start + 1 // exclusive
		for end < len(lines) && continuation(lines[end]) {
			end++
		}
		// blank and comment lines at the end belong to whatever follows
		for end > start+1 {
			if t := strings.TrimSpace(lines[end-1]); t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			end--
		}
		out := strings.Join(lines[:start], "") + block + strings.Join(lines[end:], "")
		return []byte(out), nil
	}
	out := strings.Join(lines, "")
	if block == "" {
		return []byte(out), nil
	}
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out + block), nil
}

// continuation reports whether a line still belongs to the block above it:
// indented, blank, a comment, or the closing bracket of a flow collection.
func continuation(l string) bool {
	t := strings.TrimRight(l, "\r\n")
	if t == "" || t[0] == ' ' || t[0] == '\t' || t[0] == '#' {
		return true
	}
	return t[0] == ']' || t[0] == '}'
}

func renderNotify(n Notify) (string, error) {
	str := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
	seq := func(l []string) *yaml.Node {
		s := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, v := range l {
			s.Content = append(s.Content, str(v))
		}
		return s
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string, v *yaml.Node) { m.Content = append(m.Content, str(k), v) }
	for _, l := range []struct {
		k string
		v []string
	}{{"failed", n.Failed}, {"approval", n.Approval}, {"success", n.Success}, {"drift", n.Drift}} {
		if l.v != nil {
			add(l.k, seq(l.v))
		}
	}
	if n.QuietUnder != "" {
		add("quiet_under", str(n.QuietUnder))
	}
	if n.Envs != nil {
		add("envs", seq(n.Envs))
	}
	if n.Webhook != "" {
		add("webhook", str(n.Webhook))
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{str("notify"), m}}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}
