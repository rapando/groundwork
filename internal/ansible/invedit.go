package ansible

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Structured edits to YAML inventories and vars files. Every change splices
// whole lines into the original text, so comments, blank lines, quoting and
// key order stay as written; only the lines an edit touches change.

// InvEdit is one change to a YAML inventory.
type InvEdit struct {
	Op      string // add_host | remove_host | add_group | remove_group | set_var | unset_var
	Host    string
	Group   string // the group to add to / remove from ("" with remove_host: every group)
	Parent  string // add_group: the parent group ("" = all)
	Address string // add_host: optional ansible_host
	Scope   string // set_var/unset_var: host | group
	Key     string
	Value   string // set_var: a one-line YAML value, written as typed
}

var (
	hostNameRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]*(\[[0-9a-z]+:[0-9a-z]+\][A-Za-z0-9_.\-]*)?$`)
	groupNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	varKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidHostName, ValidGroupName and ValidVarKey accept the names inventory
// edits write (a host may use a [01:10] range).
func ValidHostName(s string) bool  { return hostNameRe.MatchString(s) }
func ValidGroupName(s string) bool { return groupNameRe.MatchString(s) }
func ValidVarKey(s string) bool    { return varKeyRe.MatchString(s) }

// IsSecretKey reports whether a variable name looks like it holds a secret.
func IsSecretKey(s string) bool { return secretKey.MatchString(s) }

// EditInventory applies e to the YAML inventory src.
func EditInventory(src []byte, e InvEdit) ([]byte, error) {
	d, err := parseInv(src)
	if err != nil {
		return nil, err
	}
	if d.root != nil && d.root.Kind != yaml.MappingNode {
		return nil, errors.New("this file isn't a YAML inventory (its top level isn't a mapping of groups)")
	}
	switch e.Op {
	case "add_host":
		err = d.addHost(e)
	case "remove_host":
		err = d.removeHost(e)
	case "add_group":
		err = d.addGroup(e)
	case "remove_group":
		err = d.removeGroup(e)
	case "set_var", "unset_var":
		err = d.inventoryVar(e)
	default:
		err = fmt.Errorf("unknown edit %q", e.Op)
	}
	if err != nil {
		return nil, err
	}
	return d.result()
}

// EditVarsFile sets (value != nil) or removes (value == nil) key in a
// group_vars/host_vars YAML file. src may be empty for a new file.
func EditVarsFile(src []byte, key string, value *string) ([]byte, error) {
	if !ValidVarKey(key) {
		return nil, fmt.Errorf("invalid variable name %q", key)
	}
	if strings.HasPrefix(strings.TrimSpace(string(src)), vaultHeader) {
		return nil, errors.New("this file is vault-encrypted: edit it with ansible-vault edit")
	}
	d, err := parseInv(src)
	if err != nil {
		return nil, err
	}
	if d.root != nil && d.root.Kind != yaml.MappingNode {
		return nil, errors.New("this vars file's top level isn't a mapping")
	}
	if value == nil {
		err = d.unsetKey(nil, d.root, key, false)
	} else {
		if len(src) == 0 {
			d.lines = []string{"---"}
		}
		err = d.setKey(nil, d.root, key, *value)
	}
	if err != nil {
		return nil, err
	}
	return d.result()
}

// InventoryGroups lists the groups a YAML inventory writes, with their depth
// below all, in file order. ansible-inventory leaves out empty groups; an
// editor needs them as targets.
func InventoryGroups(src []byte) []GroupRef {
	d, err := parseInv(src)
	if err != nil || d.root == nil || d.root.Kind != yaml.MappingNode {
		return nil
	}
	var out []GroupRef
	seen := map[string]bool{}
	var walk func(m *yaml.Node, depth int)
	walk = func(m *yaml.Node, depth int) {
		if m == nil || m.Kind != yaml.MappingNode || depth > 32 {
			return
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			name, v := m.Content[i].Value, m.Content[i+1]
			dd := depth
			if name == "all" {
				dd = 0
			} else if !seen[name] {
				seen[name] = true
				out = append(out, GroupRef{name, dd})
			}
			if _, cv, _ := lookup(v, "children"); cv != nil {
				walk(cv, dd+1)
			}
		}
	}
	walk(d.root, 1)
	return out
}

// GroupRef is a group named in an inventory file.
type GroupRef struct {
	Name  string
	Depth int
}

// ---- document ----

type invDoc struct {
	lines []string
	root  *yaml.Node // top-level node, nil for an empty document
	unit  int        // indentation step
}

func parseInv(src []byte) (*invDoc, error) {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	if strings.Contains(text, "\t") && tabIndented(text) {
		return nil, errors.New("this file indents with tabs, which YAML doesn't allow")
	}
	d := &invDoc{unit: 2}
	d.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		d.lines = nil
	}
	if err := d.reparse(); err != nil {
		return nil, err
	}
	d.unit = indentUnit(d.root)
	return d, nil
}

func tabIndented(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "\t") && strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

func (d *invDoc) reparse() error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(d.lines, "\n")+"\n"), &doc); err != nil {
		return fmt.Errorf("not valid YAML: %v", err)
	}
	d.root = nil
	if len(doc.Content) > 0 {
		d.root = doc.Content[0]
		if d.root.Kind == yaml.ScalarNode && d.root.Tag == "!!null" {
			d.root = nil
		}
	}
	return nil
}

func (d *invDoc) result() ([]byte, error) {
	if err := d.reparse(); err != nil { // never hand back something that doesn't parse
		return nil, fmt.Errorf("the edit would leave invalid YAML: %v", err)
	}
	return []byte(strings.Join(d.lines, "\n") + "\n"), nil
}

// indentUnit is the file's indentation step, from its first nested block mapping.
func indentUnit(n *yaml.Node) int {
	if n == nil || n.Kind != yaml.MappingNode {
		return 2
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 {
			if u := v.Content[0].Column - k.Column; u > 0 {
				return u
			}
		}
		if u := indentUnit(v); u != 2 {
			return u
		}
	}
	return 2
}

func isNull(n *yaml.Node) bool { return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null") }

func isEmptyMap(n *yaml.Node) bool {
	return isNull(n) || (n.Kind == yaml.MappingNode && len(n.Content) == 0)
}

func lookup(m *yaml.Node, key string) (k, v *yaml.Node, idx int) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil, -1
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1], i
		}
	}
	return nil, nil, -1
}

// renderKey writes a mapping key or plain string, quoted only when YAML needs it.
func renderKey(s string) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimSuffix(string(b), "\n")
}

func blankOrComment(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || strings.HasPrefix(t, "#")
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// span returns the 0-based line range [start, end] of a block-mapping entry:
// its key line through the last line indented deeper than the key, leaving
// trailing blank and comment lines to whatever follows.
func (d *invDoc) span(k *yaml.Node) (int, int) {
	start := k.Line - 1
	col := k.Column - 1
	end := start
	for i := start + 1; i < len(d.lines); i++ {
		l := d.lines[i]
		if blankOrComment(l) {
			continue
		}
		if indentOf(l) <= col {
			break
		}
		end = i
	}
	return start, end
}

func (d *invDoc) splice(from, to int, repl []string) { // replaces lines [from, to)
	out := append([]string{}, d.lines[:from]...)
	out = append(out, repl...)
	d.lines = append(out, d.lines[to:]...)
}

func pad(n int) string { return strings.Repeat(" ", n) }

func (d *invDoc) indentBlock(base int, block []string) []string {
	out := make([]string, len(block))
	for i, l := range block {
		out[i] = pad(base) + l
	}
	return out
}

// nested renders key: on one line and each child line one step deeper.
func (d *invDoc) nested(key string, children ...string) []string {
	out := []string{renderKey(key) + ":"}
	for _, c := range children {
		out = append(out, pad(d.unit)+c)
	}
	return out
}

// insert adds block (lines relative to the mapping's indentation) as new
// entries of mapping m, the value of owner (nil = the document root).
func (d *invDoc) insert(owner, m *yaml.Node, block []string) error {
	switch {
	case owner == nil && m == nil: // empty document: append after any comments/---
		d.lines = append(d.lines, block...)
		return nil
	case m != nil && m.Kind == yaml.MappingNode && m.Style&yaml.FlowStyle == 0 && len(m.Content) > 0:
		lastKey := m.Content[len(m.Content)-2]
		_, end := d.span(lastKey)
		d.splice(end+1, end+1, d.indentBlock(m.Content[0].Column-1, block))
		return nil
	case owner != nil && isEmptyMap(m):
		// `key:`, `key: ~` or `key: {}`: becomes `key:` with a block under it
		line := owner.Line - 1
		base := owner.Column - 1
		_, end := d.span(owner)
		d.splice(line, end+1, append([]string{pad(base) + renderKey(owner.Value) + ":"}, d.indentBlock(base+d.unit, block)...))
		return nil
	case m != nil && m.Kind == yaml.MappingNode:
		return fmt.Errorf("%s is written in flow style ({…}): edit it in Code", ownerName(owner))
	default:
		return fmt.Errorf("%s isn't a mapping", ownerName(owner))
	}
}

func ownerName(owner *yaml.Node) string {
	if owner == nil {
		return "the file"
	}
	return fmt.Sprintf("%q (line %d)", owner.Value, owner.Line)
}

// remove deletes entry idx of mapping m (the value of owner). A mapping left
// empty under a key becomes `key: {}` (emptyNull: `key:`).
func (d *invDoc) remove(owner, m *yaml.Node, idx int, emptyNull bool) {
	start, end := d.span(m.Content[idx])
	if owner != nil && len(m.Content) == 2 {
		repl := pad(owner.Column-1) + renderKey(owner.Value) + ":"
		if !emptyNull {
			repl += " {}"
		}
		ostart := owner.Line - 1
		d.splice(ostart, end+1, []string{repl})
		return
	}
	d.splice(start, end+1, nil)
}

// setKey sets key in mapping m (the value of owner), replacing an existing entry in place.
func (d *invDoc) setKey(owner, m *yaml.Node, key, value string) error {
	line := renderKey(key) + ": " + value
	if err := checkOneLine(line); err != nil {
		return err
	}
	if k, _, _ := lookup(m, key); k != nil {
		if m.Style&yaml.FlowStyle != 0 {
			return fmt.Errorf("%s is written in flow style ({…}): edit it in Code", ownerName(owner))
		}
		start, end := d.span(k)
		d.splice(start, end+1, []string{pad(k.Column-1) + line})
		return nil
	}
	return d.insert(owner, m, []string{line})
}

// unsetKey removes key; a mapping left empty becomes `owner:` (emptyNull, as
// for a host) or `owner: {}`.
func (d *invDoc) unsetKey(owner, m *yaml.Node, key string, emptyNull bool) error {
	k, _, idx := lookup(m, key)
	if k == nil {
		return fmt.Errorf("%s isn't set here", key)
	}
	if m.Style&yaml.FlowStyle != 0 {
		return fmt.Errorf("%s is written in flow style ({…}): edit it in Code", ownerName(owner))
	}
	d.remove(owner, m, idx, emptyNull)
	return nil
}

// checkOneLine makes sure `key: value` is one YAML mapping entry, so a value
// like `a: b` or `[` can't change the file's structure.
func checkOneLine(line string) error {
	if strings.ContainsAny(line, "\n\r") {
		return errors.New("the value must be on one line")
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(line+"\n"), &n); err != nil || len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode || len(n.Content[0].Content) != 2 {
		return errors.New("not a valid YAML value (quote it if it contains : or #)")
	}
	return nil
}

// ---- inventory structure ----

// occurrence is one place a group is written: its key, value and the
// mapping holding it (the root or some group's children).
type occurrence struct {
	key, val, holder, holderOwner *yaml.Node
	idx                           int
}

func (d *invDoc) groups(name string) []occurrence {
	var out []occurrence
	var walk func(owner, m *yaml.Node, depth int)
	walk = func(owner, m *yaml.Node, depth int) {
		if m == nil || m.Kind != yaml.MappingNode || depth > 32 {
			return
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			k, v := m.Content[i], m.Content[i+1]
			if k.Value == name {
				out = append(out, occurrence{k, v, m, owner, i})
			}
			if ck, cv, _ := lookup(v, "children"); ck != nil {
				walk(ck, cv, depth+1)
			}
		}
	}
	walk(nil, d.root, 0)
	return out
}

// definition picks the occurrence that holds the group's data, if any.
func definition(occ []occurrence) occurrence {
	for _, o := range occ {
		if o.val.Kind == yaml.MappingNode && len(o.val.Content) > 0 {
			return o
		}
	}
	return occ[0]
}

func (d *invDoc) hostOccurrences(host string) []occurrence { // host entries, holder = a hosts mapping
	var out []occurrence
	var walk func(m *yaml.Node, depth int)
	walk = func(m *yaml.Node, depth int) {
		if m == nil || m.Kind != yaml.MappingNode || depth > 32 {
			return
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			v := m.Content[i+1]
			if hk, hv, _ := lookup(v, "hosts"); hk != nil {
				if k, val, idx := lookup(hv, host); k != nil {
					out = append(out, occurrence{k, val, hv, hk, idx})
				}
			}
			if _, cv, _ := lookup(v, "children"); cv != nil {
				walk(cv, depth+1)
			}
		}
	}
	walk(d.root, 0)
	return out
}

func (d *invDoc) groupOrAll(name string) (occurrence, error) {
	occ := d.groups(name)
	if len(occ) > 0 {
		return definition(occ), nil
	}
	if name == "all" { // implicit: write an explicit all: at the top
		if err := d.insert(nil, d.root, []string{"all: {}"}); err != nil {
			return occurrence{}, err
		}
		if err := d.reparse(); err != nil {
			return occurrence{}, err
		}
		return definition(d.groups("all")), nil
	}
	return occurrence{}, fmt.Errorf("there's no group %s in this inventory: add it first", name)
}

// ensure returns key's mapping inside group g, adding `key:` when it's missing.
func (d *invDoc) ensure(g occurrence, key, groupName string) (owner, m *yaml.Node, err error) {
	if k, v, _ := lookup(g.val, key); k != nil {
		return k, v, nil
	}
	if err := d.insert(g.key, g.val, []string{key + ": {}"}); err != nil {
		return nil, nil, err
	}
	if err := d.reparse(); err != nil {
		return nil, nil, err
	}
	ng, err := d.groupOrAll(groupName)
	if err != nil {
		return nil, nil, err
	}
	k, v, _ := lookup(ng.val, key)
	return k, v, nil
}

func (d *invDoc) addHost(e InvEdit) error {
	if !ValidHostName(e.Host) {
		return fmt.Errorf("invalid host name %q", e.Host)
	}
	group := e.Group
	if group == "" {
		group = "all"
	}
	if !ValidGroupName(group) {
		return fmt.Errorf("invalid group name %q", group)
	}
	g, err := d.groupOrAll(group)
	if err != nil {
		return err
	}
	if _, hv, _ := lookup(g.val, "hosts"); hv != nil {
		if k, _, _ := lookup(hv, e.Host); k != nil {
			return fmt.Errorf("%s is already in %s", e.Host, group)
		}
	}
	block := []string{renderKey(e.Host) + ":"}
	if e.Address != "" {
		line := "ansible_host: " + renderKey(e.Address)
		if err := checkOneLine(line); err != nil {
			return err
		}
		block = d.nested(e.Host, line)
	}
	owner, hosts, err := d.ensure(g, "hosts", group)
	if err != nil {
		return err
	}
	return d.insert(owner, hosts, block)
}

func (d *invDoc) removeHost(e InvEdit) error {
	removed := 0
	for {
		var target *occurrence
		for _, o := range d.hostOccurrences(e.Host) {
			if e.Group == "" || d.hostsBelongTo(o, e.Group) {
				target = &o
				break
			}
		}
		if target == nil {
			break
		}
		d.remove(target.holderOwner, target.holder, target.idx, false)
		removed++
		if err := d.reparse(); err != nil {
			return err
		}
	}
	if removed == 0 {
		if e.Group == "" {
			return fmt.Errorf("%s isn't listed in this file", e.Host)
		}
		return fmt.Errorf("%s isn't listed directly under %s in this file", e.Host, e.Group)
	}
	return nil
}

// hostsBelongTo reports whether host occurrence o is under group's hosts.
func (d *invDoc) hostsBelongTo(o occurrence, group string) bool {
	for _, g := range d.groups(group) {
		if _, hv, _ := lookup(g.val, "hosts"); hv == o.holder {
			return true
		}
	}
	return false
}

func (d *invDoc) addGroup(e InvEdit) error {
	if !ValidGroupName(e.Group) || e.Group == "all" || e.Group == "ungrouped" {
		return fmt.Errorf("invalid group name %q", e.Group)
	}
	if len(d.groups(e.Group)) > 0 {
		return fmt.Errorf("%s already exists", e.Group)
	}
	parent := e.Parent
	if parent == "" {
		parent = "all"
	}
	if parent == "all" && len(d.groups("all")) == 0 {
		// top-level groups are children of all
		return d.insert(nil, d.root, []string{renderKey(e.Group) + ": {}"})
	}
	if !ValidGroupName(parent) {
		return fmt.Errorf("invalid parent group %q", parent)
	}
	p, err := d.groupOrAll(parent)
	if err != nil {
		return err
	}
	owner, children, err := d.ensure(p, "children", parent)
	if err != nil {
		return err
	}
	return d.insert(owner, children, []string{renderKey(e.Group) + ": {}"})
}

func (d *invDoc) removeGroup(e InvEdit) error {
	if e.Group == "all" || e.Group == "ungrouped" {
		return fmt.Errorf("%s can't be removed", e.Group)
	}
	occ := d.groups(e.Group)
	if len(occ) == 0 {
		return fmt.Errorf("there's no group %s in this file", e.Group)
	}
	for _, o := range occ {
		for _, key := range []string{"hosts", "children", "vars"} {
			if _, v, _ := lookup(o.val, key); !isEmptyMap(v) {
				return fmt.Errorf("%s still has %s: remove or move them first", e.Group, map[string]string{"hosts": "hosts", "children": "child groups", "vars": "variables"}[key])
			}
		}
	}
	for {
		occ := d.groups(e.Group)
		if len(occ) == 0 {
			return nil
		}
		o := occ[0]
		d.remove(o.holderOwner, o.holder, o.idx, false)
		if err := d.reparse(); err != nil {
			return err
		}
	}
}

func (d *invDoc) inventoryVar(e InvEdit) error {
	if !ValidVarKey(e.Key) {
		return fmt.Errorf("invalid variable name %q", e.Key)
	}
	set := e.Op == "set_var"
	switch e.Scope {
	case "host":
		occ := d.hostOccurrences(e.Host)
		if len(occ) == 0 {
			return fmt.Errorf("%s isn't listed in this file", e.Host)
		}
		// the entry that already sets the key, else one that has vars, else the first
		pick := occ[0]
		for _, o := range occ {
			if k, _, _ := lookup(o.val, e.Key); k != nil {
				pick = o
				break
			}
			if o.val.Kind == yaml.MappingNode && len(o.val.Content) > 0 && isNull(pick.val) {
				pick = o
			}
		}
		if set {
			return d.setKey(pick.key, pick.val, e.Key, e.Value)
		}
		return d.unsetKey(pick.key, pick.val, e.Key, true)
	case "group":
		if !ValidGroupName(e.Group) {
			return fmt.Errorf("invalid group name %q", e.Group)
		}
		if !set {
			for _, o := range d.groups(e.Group) {
				if vk, vv, _ := lookup(o.val, "vars"); vk != nil {
					if k, _, _ := lookup(vv, e.Key); k != nil {
						return d.unsetKey(vk, vv, e.Key, false)
					}
				}
			}
			return fmt.Errorf("%s isn't set on %s in this file", e.Key, e.Group)
		}
		g, err := d.groupOrAll(e.Group)
		if err != nil {
			return err
		}
		if vk, vv, _ := lookup(g.val, "vars"); vk != nil {
			return d.setKey(vk, vv, e.Key, e.Value)
		}
		line := renderKey(e.Key) + ": " + e.Value
		if err := checkOneLine(line); err != nil {
			return err
		}
		return d.insert(g.key, g.val, d.nested("vars", line))
	}
	return fmt.Errorf("scope must be host or group")
}
