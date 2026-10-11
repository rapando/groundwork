package graph

import (
	"strings"
	"testing"
	"time"
)

const awsState = `{"version":4,"resources":[
 {"mode":"managed","type":"aws_instance","name":"web","instances":[
   {"index_key":0,"attributes":{"id":"i-1","private_ip":"10.0.1.10","public_ip":"","vpc_security_group_ids":["sg-web"],"tags":{"Name":"web-1"},"user_data":"secret-ish"}},
   {"index_key":1,"attributes":{"id":"i-2","private_ip":"10.0.1.11","vpc_security_group_ids":["sg-web"],"tags":{"Name":"web-2"}}}]},
 {"mode":"managed","type":"aws_eip","name":"web","instances":[
   {"attributes":{"id":"eip-1","public_ip":"203.0.113.7","private_ip":"10.0.1.10"}}]},
 {"mode":"managed","type":"aws_security_group","name":"web","instances":[
   {"attributes":{"id":"sg-web","name":"web","ingress":[
     {"protocol":"tcp","from_port":22,"to_port":22,"cidr_blocks":["10.0.0.0/8"]},
     {"protocol":"tcp","from_port":443,"to_port":443,"cidr_blocks":["0.0.0.0/0"]}]}}]},
 {"mode":"managed","type":"aws_vpc_security_group_ingress_rule","name":"app","instances":[
   {"attributes":{"security_group_id":"sg-web","ip_protocol":"tcp","from_port":8080,"to_port":8080,"cidr_ipv4":"10.0.0.0/16"}}]},
 {"module":"module.db","mode":"managed","type":"aws_db_instance","name":"main","instances":[
   {"attributes":{"id":"db-1","address":"db.internal","password":"hunter2"}}]},
 {"mode":"data","type":"aws_ami","name":"x","instances":[{"attributes":{"id":"ami-1","name":"ubuntu"}}]}
]}`

func TestParseStateKeepsOnlyAddressesAndRules(t *testing.T) {
	s, err := ParseState([]byte(awsState))
	if err != nil {
		t.Fatal(err)
	}
	byAddr := map[string]*Instance{}
	for _, in := range s.Instances {
		byAddr[in.Address] = in
	}
	web := byAddr["aws_instance.web[0]"]
	if web == nil || web.Node != "aws_instance.web" || strings.Join(web.IPs, ",") != "10.0.1.10" || strings.Join(web.Names, ",") != "web-1" {
		t.Fatalf("%+v", web)
	}
	if byAddr["data.aws_ami.x"] != nil || byAddr["aws_ami.x"] != nil {
		t.Fatal("data sources aren't machines")
	}
	if len(s.Firewalls) != 1 {
		t.Fatalf("%+v", s.Firewalls)
	}
	fw := s.Firewalls[0]
	if len(fw.Rules) != 3 || fw.Rules[2].From != 8080 || !fw.AppliesTo(web) {
		t.Fatalf("standalone ingress rules join their group: %+v", fw.Rules)
	}
	if _, err := ParseState([]byte("{")); err == nil {
		t.Fatal("bad JSON")
	}
}

func TestBuildSystemPlacesHostsAndFlagsPorts(t *testing.T) {
	st, _ := ParseState([]byte(awsState))
	arch := &Architecture{
		Nodes: []*Node{{ID: "aws_instance.web", Type: "aws_instance"}, {ID: "aws_eip.web", Type: "aws_eip"}, {ID: "aws_security_group.web", Type: "aws_security_group"}},
		Edges: []Edge{{From: "aws_instance.web", To: "aws_eip.web", Label: "instance"}},
	}
	now := time.Now()
	hosts := []SystemHost{
		{Project: "ops", Name: "web-1", Address: "10.0.1.10", // the EIP has this private ip too
			Declared:  []DeclaredPort{{22, "tcp", "ssh (ansible_port)", ""}, {8080, "tcp", "app_port", "app"}, {9100, "tcp", "exporter_port", "monitoring"}},
			Listening: []ListenPort{{22, "tcp", "0.0.0.0", "sshd"}, {8080, "tcp", "0.0.0.0", "app"}, {443, "tcp", "0.0.0.0", "nginx"}, {5432, "tcp", "127.0.0.1", "postgres"}},
			ListenAt:  &now},
		{Project: "ops", Name: "web-2", Address: "web-2"}, // by tag Name
		{Project: "ops", Name: "lost", Address: "192.0.2.1"},
	}
	sys := BuildSystem(arch, st, hosts, LoadRules(), "")
	got := sys.Servers["aws_instance.web"]
	if len(got) != 2 || got[0].Instance != "aws_instance.web[0]" || got[0].MatchedBy != "ip 10.0.1.10" || got[1].MatchedBy != "name web-2" {
		t.Fatalf("%+v", got)
	}
	if len(sys.Unlinked) != 1 || sys.Unlinked[0].Name != "lost" {
		t.Fatalf("%+v", sys.Unlinked)
	}
	flags := map[int]string{}
	for _, p := range got[0].Ports {
		flags[p.Port] = p.Flag
	}
	want := map[int]string{22: "", 443: "undeclared", 8080: "", 9100: "blocked"}
	for p, f := range want {
		if fl, ok := flags[p]; !ok || fl != f {
			t.Errorf("port %d: flag %q, want %q (%v)", p, fl, f, got[0].Ports)
		}
	}
	if _, ok := flags[5432]; ok {
		t.Error("loopback listeners aren't exposed")
	}
	// web-2 has no live data: a declared port is never "not-listening" without it
	for _, p := range got[1].Ports {
		if p.Flag == "not-listening" || (p.Port == 443 && p.Flag != "unused") {
			t.Errorf("%+v", p)
		}
	}
	if len(sys.Rules["aws_security_group.web"]) != 3 {
		t.Fatalf("%v", sys.Rules)
	}

	none := BuildSystem(arch, nil, hosts, LoadRules(), "no state")
	if len(none.Unlinked) != 3 || none.Unlinked[0].Reason != "no state" || len(none.Servers) != 0 {
		t.Fatalf("%+v", none)
	}
}

func TestNotListeningAndAmbiguity(t *testing.T) {
	st, _ := ParseState([]byte(`{"resources":[
	 {"mode":"managed","type":"hcloud_server","name":"a","instances":[{"attributes":{"id":"1","ipv4_address":"198.51.100.1","firewall_ids":[]}}]},
	 {"mode":"managed","type":"hcloud_server","name":"b","instances":[{"attributes":{"id":"2","ipv4_address":"198.51.100.1"}}]},
	 {"mode":"managed","type":"hcloud_server","name":"c","instances":[{"attributes":{"id":"3","ipv4_address":"198.51.100.3"}}]},
	 {"mode":"managed","type":"hcloud_firewall","name":"fw","instances":[{"attributes":{"id":"9","rule":[
	   {"direction":"in","protocol":"tcp","port":"80","source_ips":["0.0.0.0/0"]},
	   {"direction":"out","protocol":"tcp","port":"any"}],"apply_to":[{"server":"3"}]}}]}]}`))
	now := time.Now()
	hosts := []SystemHost{
		{Name: "dup", Address: "198.51.100.1"},
		{Name: "c", Address: "198.51.100.3", Declared: []DeclaredPort{{80, "tcp", "http_port", ""}}, ListenAt: &now},
	}
	sys := BuildSystem(&Architecture{}, st, hosts, LoadRules(), "")
	if len(sys.Unlinked) != 1 || !strings.Contains(sys.Unlinked[0].Reason, "several instances") {
		t.Fatalf("%+v", sys.Unlinked)
	}
	c := sys.Servers["hcloud_server.c"]
	if len(c) != 1 || len(c[0].Ports) != 1 || c[0].Ports[0].Flag != "not-listening" || len(c[0].Firewalls) != 1 {
		t.Fatalf("%+v", c)
	}
}

func TestFirewallProviders(t *testing.T) {
	st, err := ParseState([]byte(`{"resources":[
	 {"mode":"managed","type":"google_compute_instance","name":"vm","instances":[{"attributes":{"id":"vm","tags":["web"],"network_interface":[{"network_ip":"10.1.0.2"}]}}]},
	 {"mode":"managed","type":"google_compute_firewall","name":"web","instances":[{"attributes":{"direction":"INGRESS","target_tags":["web"],"allow":[{"protocol":"tcp","ports":["80","8000-8010"]}],"source_ranges":["0.0.0.0/0"]}}]},
	 {"mode":"managed","type":"google_compute_firewall","name":"ssh","instances":[{"attributes":{"direction":"INGRESS","target_tags":[],"allow":[{"protocol":"tcp","ports":["22"]}]}}]},
	 {"mode":"managed","type":"google_compute_firewall","name":"egress","instances":[{"attributes":{"direction":"EGRESS","allow":[{"protocol":"all"}]}}]},
	 {"mode":"managed","type":"digitalocean_droplet","name":"d","instances":[{"attributes":{"id":"42","ipv4_address":"192.0.2.9","tags":[]}}]},
	 {"mode":"managed","type":"digitalocean_firewall","name":"d","instances":[{"attributes":{"droplet_ids":[42],"inbound_rule":[{"protocol":"tcp","port_range":"all"}]}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var vm, d *Instance
	for _, in := range st.Instances {
		switch in.Type {
		case "google_compute_instance":
			vm = in
		case "digitalocean_droplet":
			d = in
		}
	}
	applies := map[string]bool{}
	for _, fw := range st.Firewalls {
		applies[fw.Node] = fw.AppliesTo(vm)
		if fw.Node == "digitalocean_firewall.d" && (!fw.AppliesTo(d) || !fw.Rules[0].AllPorts()) {
			t.Fatalf("%+v", fw)
		}
	}
	if !applies["google_compute_firewall.web"] || !applies["google_compute_firewall.ssh"] || applies["digitalocean_firewall.d"] {
		t.Fatalf("%v", applies)
	}
	sys := BuildSystem(&Architecture{}, st, []SystemHost{{Name: "d", Address: "192.0.2.9", Declared: []DeclaredPort{{5000, "tcp", "x_port", ""}}}}, LoadRules(), "")
	if h := sys.Servers["digitalocean_droplet.d"][0]; !h.OpenAll || h.Ports[0].Flag != "" {
		t.Fatalf("a rule open to every port allows everything: %+v", h)
	}
}

func TestDockerPublishedPortsAndNetwork(t *testing.T) {
	st, err := ParseState([]byte(`{"resources":[
	 {"mode":"managed","type":"docker_network","name":"dev","instances":[{"attributes":{"id":"n1","name":"infra-dev"}}]},
	 {"mode":"managed","type":"docker_container","name":"web","instances":[{"index_key":0,"attributes":{"id":"c1","name":"web-dev-1",
	   "network_data":[{"ip_address":"172.20.0.4"}],"ports":[{"internal":80,"external":8080,"ip":"0.0.0.0","protocol":"tcp"}]}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	arch := BuildArchitecture(&Graph{
		Nodes: []*Node{{ID: "docker_network.dev", Type: "docker_network"}, {ID: "docker_container.web", Type: "docker_container"}},
		Edges: []Edge{{From: "docker_network.dev", To: "docker_container.web", Label: "networks_advanced.name"}},
	}, LoadRules())
	if len(arch.Containers) != 1 || len(arch.Containers[0].Children) != 1 {
		t.Fatalf("containers sit in their docker network: %+v", arch.Containers)
	}
	sys := BuildSystem(arch, st, []SystemHost{{Name: "web-dev-1", Address: "web-dev-1", Declared: []DeclaredPort{{9000, "tcp", "app_port", ""}}}}, LoadRules(), "")
	h := sys.Servers["docker_container.web"]
	if len(h) != 1 || h[0].MatchedBy != "name web-dev-1" || len(h[0].Ports) != 2 {
		t.Fatalf("%+v", h)
	}
	if p := h[0].Ports[0]; p.Port != 80 || p.Flag != "unused" || p.Allowed[0] != "docker_container.web[0] from published on 0.0.0.0:8080" {
		t.Fatalf("%+v", p)
	}
	if p := h[0].Ports[1]; p.Port != 9000 || p.Flag != "blocked" {
		t.Fatalf("an unpublished port isn't reachable from outside: %+v", p)
	}
}

func TestVultrFirewallGroups(t *testing.T) {
	st, err := ParseState([]byte(`{"resources":[
	 {"mode":"managed","type":"vultr_instance","name":"dev","instances":[{"attributes":{"id":"v1","main_ip":"139.84.236.40","hostname":"p2p-dev","firewall_group_id":"fg1"}}]},
	 {"mode":"managed","type":"vultr_firewall_rule","name":"web","instances":[
	   {"index_key":"http","attributes":{"firewall_group_id":"fg1","protocol":"tcp","port":"80","subnet":"0.0.0.0","subnet_size":0}},
	   {"index_key":"range","attributes":{"firewall_group_id":"fg1","protocol":"tcp","port":"8000:8010","subnet":"10.0.0.0","subnet_size":8}}]},
	 {"mode":"managed","type":"vultr_firewall_group","name":"dev","instances":[{"index_key":0,"attributes":{"id":"fg1","description":"dev"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	sys := BuildSystem(&Architecture{}, st, []SystemHost{{Name: "p2p-dev", Address: "139.84.236.40",
		Declared: []DeclaredPort{{80, "tcp", "http_port", ""}, {8005, "tcp", "api_port", ""}, {22, "tcp", "ssh (ansible_port)", ""}}}}, LoadRules(), "")
	h := sys.Servers["vultr_instance.dev"]
	if len(h) != 1 || strings.Join(h[0].Firewalls, ",") != "vultr_firewall_group.dev[0]" {
		t.Fatalf("%+v", h)
	}
	flags := map[int]string{}
	for _, p := range h[0].Ports {
		flags[p.Port] = p.Flag
	}
	if flags[80] != "" || flags[8005] != "" || flags[22] != "blocked" {
		t.Fatalf("rules declared after their group still apply; 8000:8010 is a range: %v", flags)
	}
}
