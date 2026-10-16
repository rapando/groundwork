package graph

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SystemHost is one Ansible host as the system view needs it: what it is
// called, where it is reached, and what runs on it.
type SystemHost struct {
	Project   string         `json:"project"`
	Env       string         `json:"env"`
	Name      string         `json:"name"`
	Address   string         `json:"address"`
	OS        string         `json:"os,omitempty"`
	Roles     []RoleUse      `json:"roles"`
	Groups    []HostGroup    `json:"groups"` // inventory groups that list the host directly
	Declared  []DeclaredPort `json:"-"`
	Listening []ListenPort   `json:"-"`
	ListenAt  *time.Time     `json:"listen_at,omitempty"` // when listening ports were last gathered (nil = never)
}

// HostGroup is an inventory group a host is listed in, with the groups above
// it (outermost first, "all" left out).
type HostGroup struct {
	Name string   `json:"name"`
	Path []string `json:"path,omitempty"`
}

// RoleUse is a role applied to a host and the playbooks that apply it.
type RoleUse struct {
	Name      string   `json:"name"`
	Playbooks []string `json:"playbooks"`
}

// DeclaredPort is a port Ansible configures, from a *_port(s) variable.
type DeclaredPort struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"`
	Var   string `json:"var"`
	Role  string `json:"role,omitempty"` // the role whose defaults/vars set it
}

// service names what uses a declared port: its role, else the variable
// without the _port suffix ("ssh" for ansible_port).
func (d DeclaredPort) service() string {
	switch {
	case d.Role != "":
		return d.Role
	case strings.HasPrefix(d.Var, "ssh "):
		return "ssh"
	}
	return strings.TrimSuffix(strings.TrimSuffix(d.Var, "_ports"), "_port")
}

// ListenPort is a socket found listening on the host.
type ListenPort struct {
	Port    int    `json:"port"`
	Proto   string `json:"proto"`
	Address string `json:"address"`
	Process string `json:"process,omitempty"`
}

func (l ListenPort) local() bool {
	return strings.HasPrefix(l.Address, "127.") || l.Address == "::1" || l.Address == "localhost"
}

// PortRow lines up what Ansible declares, what firewalls allow and what is
// listening for one port on one host.
type PortRow struct {
	Port      int      `json:"port"`
	Proto     string   `json:"proto"`
	Declared  []string `json:"declared"`  // "app_port (role app)"
	Allowed   []string `json:"allowed"`   // "aws_security_group.web from 0.0.0.0/0"
	Listening []string `json:"listening"` // "nginx on 0.0.0.0"
	Services  []string `json:"services"`  // what uses the port: roles, processes ("app", "nginx")
	// Flag: blocked (declared, but no attached firewall allows it),
	// not-listening (declared, live data says nothing listens), undeclared
	// (listening and reachable but not declared), unused (a firewall opens it
	// but nothing declares or listens on it). Empty when consistent.
	Flag string `json:"flag,omitempty"`
}

// LinkedHost is an Ansible host placed on a Terraform instance.
type LinkedHost struct {
	SystemHost
	Instance  string    `json:"instance"`   // aws_instance.web[0]
	MatchedBy string    `json:"matched_by"` // "ip 10.0.0.5" | "name web-1"
	Ports     []PortRow `json:"ports"`
	Firewalls []string  `json:"firewalls"`          // firewall nodes attached to the instance
	OpenAll   bool      `json:"open_all,omitempty"` // a rule allows every port
}

// UnlinkedHost is an Ansible host no Terraform instance matched. Its ports
// are what it declares and listens on; no firewall is known for it.
type UnlinkedHost struct {
	SystemHost
	Reason string    `json:"reason"`
	Ports  []PortRow `json:"ports"`
}

func unlinked(h SystemHost, reason string) UnlinkedHost {
	ports, _ := reconcile(h, nil)
	return UnlinkedHost{SystemHost: h, Reason: reason, Ports: ports}
}

// System is the architecture view with Ansible hosts placed on the servers
// Terraform creates.
type System struct {
	*Architecture
	Servers  map[string][]LinkedHost `json:"servers"` // graph node id → hosts on its instances
	Unlinked []UnlinkedHost          `json:"unlinked"`
	Rules    map[string][]Rule       `json:"rules"` // firewall node id → inbound rules
}

// BuildSystem places hosts on state instances by address (IP first, then
// name), and reconciles their ports with the firewalls attached to them.
// st may be nil (state unavailable): every host is then unlinked.
func BuildSystem(arch *Architecture, st *State, hosts []SystemHost, rules Rules, noState string) *System {
	s := &System{Architecture: arch, Servers: map[string][]LinkedHost{}, Unlinked: []UnlinkedHost{}, Rules: map[string][]Rule{}}
	if st == nil {
		for _, h := range hosts {
			s.Unlinked = append(s.Unlinked, unlinked(h, noState))
		}
		return s
	}
	isServer := map[string]bool{}
	for _, t := range rules.Servers {
		isServer[t] = true
	}
	for _, fw := range st.Firewalls {
		s.Rules[fw.Node] = append(s.Rules[fw.Node], fw.Rules...)
	}
	byIP, byName := map[string][]*Instance{}, map[string][]*Instance{}
	byAddr := map[string]*Instance{}
	for _, in := range st.Instances {
		byAddr[in.Address] = in
		for _, ip := range in.IPs {
			byIP[ip] = append(byIP[ip], in)
		}
		for _, n := range in.Names {
			byName[strings.ToLower(n)] = append(byName[strings.ToLower(n)], in)
		}
	}
	// a non-server match (EIP, NIC) moves to the server that references it
	dependents := map[string][]string{}
	for _, e := range arch.Edges {
		dependents[e.From] = append(dependents[e.From], e.To)
	}
	nodeType := map[string]string{}
	for _, n := range arch.Nodes {
		nodeType[n.ID] = n.Type
	}
	toServer := func(in *Instance) *Instance {
		if isServer[in.Type] {
			return in
		}
		suffix := strings.TrimPrefix(in.Address, in.Node)
		for _, d := range dependents[in.Node] {
			if isServer[nodeType[d]] {
				if t := byAddr[d+suffix]; t != nil {
					return t
				}
				if t := byAddr[d]; t != nil {
					return t
				}
			}
		}
		return in
	}
	pick := func(cands []*Instance) (*Instance, bool) {
		seen := map[string]*Instance{}
		for _, c := range cands {
			c = toServer(c)
			seen[c.Address] = c
		}
		var servers, all []*Instance
		for _, c := range seen {
			all = append(all, c)
			if isServer[c.Type] {
				servers = append(servers, c)
			}
		}
		if len(servers) == 1 {
			return servers[0], true
		}
		if len(servers) == 0 && len(all) == 1 {
			return all[0], true
		}
		return nil, len(all) > 1
	}

	for _, h := range hosts {
		var in *Instance
		var by string
		ambiguous := false
		if c := byIP[h.Address]; len(c) > 0 {
			in, ambiguous = pick(c)
			by = "ip " + h.Address
		}
		if in == nil && !ambiguous {
			for _, n := range []string{h.Address, h.Name} {
				if c := byName[strings.ToLower(n)]; len(c) > 0 {
					in, ambiguous = pick(c)
					by = "name " + n
					break
				}
			}
		}
		if in == nil {
			reason := fmt.Sprintf("no instance in state has address %s or name %s", h.Address, h.Name)
			if ambiguous {
				reason = "several instances match " + strings.TrimPrefix(strings.TrimPrefix(by, "ip "), "name ")
			}
			s.Unlinked = append(s.Unlinked, unlinked(h, reason))
			continue
		}
		lh := LinkedHost{SystemHost: h, Instance: in.Address, MatchedBy: by, Firewalls: []string{}}
		var fws []*Firewall
		for _, fw := range st.Firewalls {
			if fw.AppliesTo(in) {
				fws = append(fws, fw)
				lh.Firewalls = append(lh.Firewalls, fw.Address)
			}
		}
		lh.Ports, lh.OpenAll = reconcile(h, fws)
		s.Servers[in.Node] = append(s.Servers[in.Node], lh)
	}
	for _, l := range s.Servers {
		sort.Slice(l, func(i, j int) bool {
			return l[i].Instance < l[j].Instance || l[i].Instance == l[j].Instance && l[i].Name < l[j].Name
		})
	}
	return s
}

// reconcile builds the port table for one host.
func reconcile(h SystemHost, fws []*Firewall) ([]PortRow, bool) {
	type key struct {
		port  int
		proto string
	}
	rows := map[key]*PortRow{}
	row := func(port int, proto string) *PortRow {
		k := key{port, proto}
		if rows[k] == nil {
			rows[k] = &PortRow{Port: port, Proto: proto, Declared: []string{}, Allowed: []string{}, Listening: []string{}, Services: []string{}}
		}
		return rows[k]
	}
	for _, d := range h.Declared {
		label := d.Var
		if d.Role != "" {
			label += " (role " + d.Role + ")"
		}
		r := row(d.Port, d.Proto)
		r.Declared = append(r.Declared, label)
		r.Services = append(r.Services, d.service())
	}
	for _, l := range h.Listening {
		if l.local() {
			continue
		}
		label := "on " + l.Address
		if l.Process != "" {
			label = l.Process + " " + label
		}
		r := row(l.Port, l.Proto)
		r.Listening = append(r.Listening, label)
		if l.Process != "" {
			r.Services = append(r.Services, l.Process)
		}
	}
	openAll := false
	for _, fw := range fws {
		for _, rule := range fw.Rules {
			if rule.Proto == "icmp" {
				continue
			}
			if rule.AllPorts() {
				openAll = true
				continue
			}
			if rule.To-rule.From > 64 { // a wide range is shown with the rules, not as rows
				continue
			}
			for p := rule.From; p <= rule.To; p++ {
				for _, proto := range []string{"tcp", "udp"} {
					if rule.Proto == proto || (rule.Proto == "all" && proto == "tcp") {
						row(p, proto)
					}
				}
			}
		}
	}
	out := make([]PortRow, 0, len(rows))
	for _, r := range rows {
		for _, fw := range fws {
			for _, rule := range fw.Rules {
				if rule.allows(r.Port, r.Proto) {
					label := fw.Address
					if len(rule.Sources) > 0 {
						label += " from " + strings.Join(rule.Sources, ", ")
					}
					r.Allowed = append(r.Allowed, label)
				}
			}
		}
		r.Allowed = uniq(r.Allowed)
		r.Services = uniq(r.Services)
		live := h.ListenAt != nil
		switch {
		case len(r.Declared) > 0 && len(fws) > 0 && len(r.Allowed) == 0:
			r.Flag = "blocked"
		case len(r.Declared) > 0 && live && len(r.Listening) == 0:
			r.Flag = "not-listening"
		case len(r.Declared) == 0 && len(r.Listening) > 0 && len(r.Allowed) > 0:
			r.Flag = "undeclared"
		case len(r.Declared) == 0 && len(r.Listening) == 0 && len(r.Allowed) > 0:
			r.Flag = "unused"
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Port < out[j].Port || out[i].Port == out[j].Port && out[i].Proto < out[j].Proto
	})
	return out, openAll
}
