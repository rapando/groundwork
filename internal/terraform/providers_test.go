package terraform

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredProviders(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "versions.tf"), `terraform {
  required_version = ">= 1.6"
  required_providers {
    aws    = { source = "hashicorp/aws", version = "~> 5.0" }
    random = { source = "hashicorp/random" }
    legacy = "~> 1.0"
  }
}`)
	write(t, filepath.Join(dir, "main.tf"), `resource "random_id" "x" { byte_length = 4 }`)
	write(t, filepath.Join(dir, "sub", "other.tf"), `terraform { required_providers { nope = { source = "a/b", version = "1" } } }`)
	got, err := RequiredProviders(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Requirement{
		{Name: "aws", Source: "hashicorp/aws", Version: "~> 5.0"},
		{Name: "legacy", Version: "~> 1.0"},
		{Name: "random", Source: "hashicorp/random"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if !got[0].Complete() || got[1].Complete() || got[2].Complete() {
		t.Fatal("complete means source and version")
	}

	write(t, filepath.Join(dir, "broken.tf"), `terraform { required_providers { aws = { source = `)
	if _, err := RequiredProviders(dir); err == nil {
		t.Fatal("half an edit is an error, not an empty list")
	}
}

func TestLockedProvidersAndSameProvider(t *testing.T) {
	dir := t.TempDir()
	if l, err := LockedProviders(dir); err != nil || l != nil {
		t.Fatalf("no lock file is no providers: %v %v", l, err)
	}
	write(t, filepath.Join(dir, ".terraform.lock.hcl"), `# This file is maintained automatically by "terraform init".

provider "registry.terraform.io/hashicorp/aws" {
  version     = "5.31.0"
  constraints = "~> 5.0"
  hashes = [
    "h1:abc=",
  ]
}

provider "registry.terraform.io/hashicorp/random" {
  version = "3.6.0"
  hashes = [
    "h1:def=",
  ]
}
`)
	got, err := LockedProviders(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []LockedProvider{
		{Address: "registry.terraform.io/hashicorp/aws", Version: "5.31.0", Constraints: "~> 5.0"},
		{Address: "registry.terraform.io/hashicorp/random", Version: "3.6.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	for _, c := range []struct {
		src, addr string
		same      bool
	}{
		{"hashicorp/aws", "registry.terraform.io/hashicorp/aws", true},
		{"HashiCorp/AWS", "registry.opentofu.org/hashicorp/aws", true},
		{"registry.terraform.io/hashicorp/aws", "registry.terraform.io/hashicorp/aws", true},
		{"example.com/hashicorp/aws", "registry.terraform.io/hashicorp/aws", false},
		{"hashicorp/aws", "registry.terraform.io/hashicorp/awscc", false},
	} {
		if SameProvider(c.src, c.addr) != c.same {
			t.Errorf("SameProvider(%q, %q) != %v", c.src, c.addr, c.same)
		}
	}
}

func TestPruneProvidersKeepsOnlyTheLockedVersions(t *testing.T) {
	data := t.TempDir()
	base := filepath.Join(data, "providers", "registry.terraform.io", "hashicorp")
	write(t, filepath.Join(base, "aws", "5.31.0", "darwin_arm64", "terraform-provider-aws"), "bin")
	write(t, filepath.Join(base, "aws", "5.30.0", "darwin_arm64", "terraform-provider-aws"), "old")
	write(t, filepath.Join(base, "random", "3.6.0", "darwin_arm64", "terraform-provider-random"), "bin")
	write(t, filepath.Join(data, "providers", "example.com", "acme", "thing", "1.0.0", "x", "bin"), "bin")
	// a link into a shared plugin cache goes; the cache stays
	cache := t.TempDir()
	write(t, filepath.Join(cache, "tls-bin"), "cached")
	if err := os.MkdirAll(filepath.Join(base, "tls"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cache, filepath.Join(base, "tls", "4.0.0")); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneProviders(data, []LockedProvider{{Address: "registry.terraform.io/hashicorp/aws", Version: "5.31.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"registry.terraform.io/hashicorp/random", "registry.terraform.io/hashicorp/tls", "example.com/acme/thing"}) &&
		!reflect.DeepEqual(removed, []string{"example.com/acme/thing", "registry.terraform.io/hashicorp/random", "registry.terraform.io/hashicorp/tls"}) {
		t.Fatalf("removed %v", removed)
	}
	if _, err := os.Stat(filepath.Join(base, "aws", "5.31.0")); err != nil {
		t.Fatal("the locked version stays")
	}
	for _, gone := range []string{filepath.Join(base, "aws", "5.30.0"), filepath.Join(base, "random"), filepath.Join(data, "providers", "example.com")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, "tls-bin")); err != nil {
		t.Fatal("a shared plugin cache is never touched")
	}
	if r, err := PruneProviders(t.TempDir(), nil); err != nil || r != nil {
		t.Fatalf("nothing installed: %v %v", r, err)
	}
}
