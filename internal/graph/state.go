package graph

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// State is Terraform state reduced to what the system view needs: how each
// instance is addressed and which firewall rules reach it. Attribute values
// (which can hold secrets) are dropped as soon as they are read.
type State struct {
	Instances []*Instance `json:"instances"`
	Firewalls []*Firewall `json:"firewalls"`
}

// Instance is one resource instance, e.g. aws_instance.web[0].
type Instance struct {
	Address string          `json:"address"`
	Node    string          `json:"node"` // base address: the graph node id
	Type    string          `json:"type"`
	IPs     []string        `json:"ips"`
	Names   []string        `json:"names"`
	keys    map[string]bool // provider-scoped ids, tags and firewall ids, for firewall attachment
}

// Firewall is a security group / firewall resource and its inbound rules.
type Firewall struct {
	Address string          `json:"address"`
	Node    string          `json:"node"`
	Rules   []Rule          `json:"rules"`
	targets map[string]bool // instance keys it applies to
	all     bool            // applies to every instance of its provider (GCP without target tags)
}

// Rule allows inbound traffic to a port range. Proto is tcp, udp, icmp or all.
type Rule struct {
	Proto   string   `json:"proto"`
	From    int      `json:"from"`
	To      int      `json:"to"`
	Sources []string `json:"sources,omitempty"`
}

func (r Rule) allows(port int, proto string) bool {
	return (r.Proto == "all" || r.Proto == proto) && port >= r.From && port <= r.To
}

// AllPorts reports whether the rule opens every port.
func (r Rule) AllPorts() bool { return r.From <= 1 && r.To >= 65535 }

// nameKeys are attribute names whose value names the machine.
var nameKeys = map[string]bool{"name": true, "Name": true, "hostname": true, "fqdn": true, "public_dns": true, "private_dns": true, "computer_name": true}

type rawState struct {
	Resources []struct {
		Module    string `json:"module"`
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Instances []struct {
			IndexKey   any            `json:"index_key"`
			Attributes map[string]any `json:"attributes"`
		} `json:"instances"`
	} `json:"resources"`
}

// ParseState reads `terraform state pull` output.
func ParseState(b []byte) (*State, error) {
	var raw rawState
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	s := &State{Instances: []*Instance{}, Firewalls: []*Firewall{}}
	fwByID := map[string]*Firewall{} // provider:id → firewall, for standalone rule resources
	type pending struct {
		fw   string // provider:id of the firewall
		rule *Rule
		tgts []string
	}
	var later []pending
	for _, r := range raw.Resources {
		if r.Mode != "managed" {
			continue
		}
		node := r.Type + "." + r.Name
		if r.Module != "" {
			node = r.Module + "." + node
		}
		prov := provider(r.Type)
		for _, in := range r.Instances {
			addr := node
			switch k := in.IndexKey.(type) {
			case float64:
				addr += "[" + strconv.Itoa(int(k)) + "]"
			case string:
				addr += "[" + strconv.Quote(k) + "]"
			}
			at := in.Attributes
			switch r.Type {
			case "aws_security_group_rule":
				if str(at["type"]) == "ingress" {
					later = append(later, pending{fw: "aws:" + str(at["security_group_id"]), rule: awsRule(at["protocol"], at["from_port"], at["to_port"], strs(at["cidr_blocks"]), strs(at["ipv6_cidr_blocks"]), []string{str(at["source_security_group_id"])})})
				}
				continue
			case "aws_vpc_security_group_ingress_rule":
				later = append(later, pending{fw: "aws:" + str(at["security_group_id"]), rule: awsRule(at["ip_protocol"], at["from_port"], at["to_port"], []string{str(at["cidr_ipv4"]), str(at["cidr_ipv6"])}, nil, []string{str(at["referenced_security_group_id"])})})
				continue
			case "vultr_firewall_rule":
				from, to := portRange(str(at["port"]))
				src := str(at["subnet"])
				if n := str(at["subnet_size"]); src != "" && n != "" {
					src += "/" + n
				}
				later = append(later, pending{fw: "vultr:" + str(at["firewall_group_id"]), rule: &Rule{Proto: proto(str(at["protocol"])), From: from, To: to, Sources: []string{src}}})
				continue
			case "hcloud_firewall_attachment":
				later = append(later, pending{fw: "hcloud:" + str(at["firewall_id"]), tgts: prefixed("hcloud:id:", strs(at["server_ids"]))})
				continue
			}
			if fw := firewallOf(r.Type, at); fw != nil {
				fw.Address, fw.Node = addr, node
				s.Firewalls = append(s.Firewalls, fw)
				if id := str(at["id"]); id != "" {
					fwByID[prov+":"+id] = fw
				}
				continue
			}
			inst := &Instance{Address: addr, Node: node, Type: r.Type, keys: instanceKeys(prov, at)}
			if fw := published(r.Type, at); fw != nil { // a container's published ports are its way in
				fw.Address, fw.Node = addr, node
				fw.targets[prov+":id:"+str(at["id"])] = true
				s.Firewalls = append(s.Firewalls, fw)
			}
			collect(at, "", inst)
			if len(inst.IPs) == 0 && len(inst.Names) == 0 {
				continue
			}
			inst.IPs, inst.Names = uniq(inst.IPs), uniq(inst.Names)
			s.Instances = append(s.Instances, inst)
		}
	}
	for _, p := range later {
		fw := fwByID[p.fw]
		if fw == nil {
			continue
		}
		if p.rule != nil {
			fw.Rules = append(fw.Rules, *p.rule)
		}
		for _, t := range p.tgts {
			fw.targets[t] = true
		}
	}
	return s, nil
}

// collect gathers IP addresses and machine names from any depth of attributes.
func collect(v any, key string, inst *Instance) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			collect(c, k, inst)
		}
	case []any:
		for _, c := range x {
			collect(c, key, inst)
		}
	case string:
		if x == "" {
			return
		}
		if ip := net.ParseIP(x); ip != nil && !ip.IsUnspecified() {
			inst.IPs = append(inst.IPs, x)
		} else if nameKeys[key] {
			inst.Names = append(inst.Names, x)
		}
	}
}

func provider(typ string) string {
	if i := strings.Index(typ, "_"); i > 0 {
		return typ[:i]
	}
	return typ
}

// instanceKeys: what firewalls point at to apply to this instance.
func instanceKeys(prov string, at map[string]any) map[string]bool {
	k := map[string]bool{}
	add := func(kind string, vals []string) {
		for _, v := range vals {
			if v != "" {
				k[prov+":"+kind+":"+v] = true
			}
		}
	}
	add("id", []string{str(at["id"])})
	switch prov {
	case "aws":
		add("sg", strs(at["vpc_security_group_ids"]))
		add("sgname", strs(at["security_groups"]))
	case "hcloud":
		add("fw", strs(at["firewall_ids"]))
	case "google", "digitalocean":
		add("tag", strs(at["tags"]))
	case "vultr":
		add("fw", []string{str(at["firewall_group_id"])})
	}
	return k
}

// firewallOf reads a firewall resource's inbound rules and targets, or
// returns nil when the type isn't one.
func firewallOf(typ string, at map[string]any) *Firewall {
	fw := &Firewall{Rules: []Rule{}, targets: map[string]bool{}}
	id := str(at["id"])
	switch typ {
	case "aws_security_group":
		for _, in := range maps(at["ingress"]) {
			fw.Rules = append(fw.Rules, *awsRule(in["protocol"], in["from_port"], in["to_port"], strs(in["cidr_blocks"]), strs(in["ipv6_cidr_blocks"]), strs(in["security_groups"])))
		}
		fw.targets["aws:sg:"+id] = true
		if n := str(at["name"]); n != "" {
			fw.targets["aws:sgname:"+n] = true
		}
	case "hcloud_firewall":
		for _, r := range maps(at["rule"]) {
			if str(r["direction"]) != "in" {
				continue
			}
			from, to := portRange(str(r["port"]))
			fw.Rules = append(fw.Rules, Rule{Proto: proto(str(r["protocol"])), From: from, To: to, Sources: strs(r["source_ips"])})
		}
		for _, a := range maps(at["apply_to"]) {
			if s := str(a["server"]); s != "" && s != "0" {
				fw.targets["hcloud:id:"+s] = true
			}
		}
		fw.targets["hcloud:fw:"+id] = true
	case "google_compute_firewall":
		if d := str(at["direction"]); d != "" && d != "INGRESS" {
			return fw
		}
		for _, a := range maps(at["allow"]) {
			ports := strs(a["ports"])
			if len(ports) == 0 {
				ports = []string{""}
			}
			for _, p := range ports {
				from, to := portRange(p)
				fw.Rules = append(fw.Rules, Rule{Proto: proto(str(a["protocol"])), From: from, To: to, Sources: strs(at["source_ranges"])})
			}
		}
		tags := strs(at["target_tags"])
		fw.all = len(tags) == 0
		for _, t := range tags {
			fw.targets["google:tag:"+t] = true
		}
	case "vultr_firewall_group": // rules are separate vultr_firewall_rule resources
		fw.targets["vultr:fw:"+id] = true
	case "digitalocean_firewall":
		for _, r := range maps(at["inbound_rule"]) {
			from, to := portRange(str(r["port_range"]))
			fw.Rules = append(fw.Rules, Rule{Proto: proto(str(r["protocol"])), From: from, To: to, Sources: strs(r["source_addresses"])})
		}
		for _, d := range strs(at["droplet_ids"]) {
			fw.targets["digitalocean:id:"+d] = true
		}
		for _, t := range strs(at["tags"]) {
			fw.targets["digitalocean:tag:"+t] = true
		}
	default:
		return nil
	}
	return fw
}

// published reads docker_container ports { internal, external, ip, protocol }
// as the container's inbound rules, or returns nil for other resources.
func published(typ string, at map[string]any) *Firewall {
	ports := maps(at["ports"])
	if typ != "docker_container" || len(ports) == 0 {
		return nil
	}
	fw := &Firewall{Rules: []Rule{}, targets: map[string]bool{}}
	for _, p := range ports {
		in := num(p["internal"])
		src := "published"
		if ext := num(p["external"]); ext > 0 {
			src = "published on " + strings.TrimSuffix(str(p["ip"])+":", "::") + strconv.Itoa(ext)
		}
		fw.Rules = append(fw.Rules, Rule{Proto: proto(str(p["protocol"])), From: in, To: in, Sources: []string{src}})
	}
	return fw
}

// AppliesTo reports whether the firewall's rules reach the instance.
func (fw *Firewall) AppliesTo(in *Instance) bool {
	if fw.all && provider(in.Type) == provider(firstType(fw.Node)) {
		return true
	}
	for k := range in.keys {
		if fw.targets[k] {
			return true
		}
	}
	return false
}

func firstType(node string) string {
	parts := strings.Split(node, ".")
	if len(parts) < 2 {
		return node
	}
	return parts[len(parts)-2]
}

func awsRule(protocol, from, to any, cidr, cidr6, groups []string) *Rule {
	r := &Rule{Proto: proto(str(protocol)), From: num(from), To: num(to)}
	if r.Proto == "all" || (r.From == 0 && r.To == 0 && r.Proto != "icmp") {
		r.From, r.To = 0, 65535
	}
	for _, l := range [][]string{cidr, cidr6, groups} {
		for _, s := range l {
			if s != "" {
				r.Sources = append(r.Sources, s)
			}
		}
	}
	return r
}

// proto normalises protocol spellings (AWS "-1", numbers, "TCP").
func proto(p string) string {
	switch strings.ToLower(p) {
	case "-1", "all", "any", "":
		return "all"
	case "6", "tcp":
		return "tcp"
	case "17", "udp":
		return "udp"
	case "1", "icmp", "icmpv6", "58":
		return "icmp"
	}
	return strings.ToLower(p)
}

// portRange reads "22", "8000-8100", "8000:8100" (Vultr), "any", "all" or "" (every port).
func portRange(s string) (int, int) {
	s = strings.TrimSpace(s)
	if s == "" || s == "any" || s == "all" {
		return 0, 65535
	}
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		a, b, ok = strings.Cut(s, ":")
	}
	if ok {
		from, _ := strconv.Atoi(strings.TrimSpace(a))
		to, _ := strconv.Atoi(strings.TrimSpace(b))
		return from, to
	}
	p, _ := strconv.Atoi(s)
	return p, p
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func strs(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s := str(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func maps(v any) []map[string]any {
	l, _ := v.([]any)
	out := make([]map[string]any, 0, len(l))
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func prefixed(p string, l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = p + s
	}
	return out
}

func uniq(l []string) []string {
	sort.Strings(l)
	out := l[:0]
	for i, s := range l {
		if i == 0 || s != l[i-1] {
			out = append(out, s)
		}
	}
	return out
}
