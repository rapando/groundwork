package scaffold

import (
	"regexp"
	"strings"
)

// Cloud is a provider authenticated by an API token from the environment.
// These presets share the templates in presets/cloud/terraform and only
// bring their own network module (presets/cloud/<preset>/), so adding one
// is a table entry plus two small files.
type Cloud struct {
	Label       string // shown in the UI and descriptions
	Name        string // provider local name in HCL
	Source      string
	Version     string
	TokenEnv    string // environment variable the provider reads its token from
	RegionTerm  string // what the provider calls the place a network lives
	RegionHint  string // an example value
	HostExample string // HCL expression for a server's private IP, for the outputs comment
	Storage     string // the provider's S3-compatible object storage, for the s3 backend
	S3Hint      string // an example endpoint
	S3PathStyle bool   // the endpoint wants path-style bucket URLs
	S3Lock      bool   // the storage supports the conditional writes use_lockfile needs
	region      *regexp.Regexp
}

// S3Region is the region the s3 backend signs requests with: the first label
// of the endpoint's host (fsn1, us-east-1, fra1, ...), which is what these
// providers expect, or ignore.
func S3Region(endpoint string) string {
	h := strings.TrimPrefix(endpoint, "https://")
	if i := strings.IndexAny(h, ".:/"); i > 0 {
		h = h[:i]
	}
	return h
}

// Clouds holds the token-authenticated presets, keyed by preset name.
var Clouds = map[string]Cloud{
	"vultr": {
		Label: "Vultr", Name: "vultr", Source: "vultr/vultr", Version: "~> 2.0", TokenEnv: "VULTR_API_KEY",
		RegionTerm: "region", RegionHint: "ams", HostExample: "vultr_instance.web.internal_ip",
		Storage: "Vultr Object Storage", S3Hint: "https://ams1.vultrobjects.com",
		region: regexp.MustCompile(`^[a-z]{3}$`),
	},
	"digitalocean": {
		Label: "DigitalOcean", Name: "digitalocean", Source: "digitalocean/digitalocean", Version: "~> 2.0", TokenEnv: "DIGITALOCEAN_TOKEN",
		RegionTerm: "region", RegionHint: "fra1", HostExample: "digitalocean_droplet.web.ipv4_address_private",
		Storage: "DigitalOcean Spaces", S3Hint: "https://fra1.digitaloceanspaces.com",
		region: regexp.MustCompile(`^[a-z]{3}\d$`),
	},
	"hetzner": {
		Label: "Hetzner Cloud", Name: "hcloud", Source: "hetznercloud/hcloud", Version: "~> 1.45", TokenEnv: "HCLOUD_TOKEN",
		RegionTerm: "network zone", RegionHint: "eu-central", HostExample: "one(hcloud_server.web.network).ip",
		Storage: "Hetzner Object Storage", S3Hint: "https://fsn1.your-objectstorage.com", S3PathStyle: true, S3Lock: true,
		region: regexp.MustCompile(`^(eu-central|us-east|us-west|ap-southeast)$`),
	},
	"linode": {
		Label: "Linode (Akamai)", Name: "linode", Source: "linode/linode", Version: "~> 2.0", TokenEnv: "LINODE_TOKEN",
		RegionTerm: "region", RegionHint: "eu-central", HostExample: "linode_instance.web.private_ip_address",
		Storage: "Linode Object Storage", S3Hint: "https://eu-central-1.linodeobjects.com",
		region: regexp.MustCompile(`^[a-z]{2}-[a-z]+(-\d)?$`),
	},
}
