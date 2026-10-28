package terraform

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// Requirement is one entry of a required_providers block.
type Requirement struct {
	Name    string // the local name: aws in `aws = { ... }`
	Source  string // hashicorp/aws, as written
	Version string // the constraint, as written
}

// Complete reports whether both source and version are set: groundwork
// installs a provider only once both are written.
func (r Requirement) Complete() bool { return r.Source != "" && r.Version != "" }

// RequiredProviders reads the required_providers entries of the .tf files
// directly in dir. A file that doesn't parse is an error: the caller waits
// for the next save rather than act on half an edit.
func RequiredProviders(dir string) ([]Requirement, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Requirement
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		f, diags := hclsyntax.ParseConfig(src, e.Name(), hcl.InitialPos)
		if diags.HasErrors() {
			return nil, fmt.Errorf("%s: %s", e.Name(), diags.Errs()[0])
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, b := range body.Blocks {
			if b.Type != "terraform" {
				continue
			}
			for _, rp := range b.Body.Blocks {
				if rp.Type != "required_providers" {
					continue
				}
				for name, attr := range rp.Body.Attributes {
					out = append(out, requirement(name, attr))
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func requirement(name string, attr *hclsyntax.Attribute) Requirement {
	r := Requirement{Name: name}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() {
		return r
	}
	switch {
	case v.Type() == cty.String: // the pre-0.13 form: aws = "~> 5.0"
		r.Version = v.AsString()
	case v.Type().IsObjectType():
		str := func(k string) string {
			if !v.Type().HasAttribute(k) {
				return ""
			}
			a := v.GetAttr(k)
			if a.IsNull() || a.Type() != cty.String {
				return ""
			}
			return strings.TrimSpace(a.AsString())
		}
		r.Source, r.Version = str("source"), str("version")
	}
	return r
}

// LockedProvider is one provider in .terraform.lock.hcl.
type LockedProvider struct {
	Address     string // registry.terraform.io/hashicorp/aws
	Version     string // 5.31.0
	Constraints string // ~> 5.0
}

var (
	lockBlock = regexp.MustCompile(`(?m)^provider\s+"([^"]+)"\s*\{`)
	lockAttr  = regexp.MustCompile(`(?m)^\s*(version|constraints)\s*=\s*"([^"]*)"`)
)

// LockedProviders reads dir/.terraform.lock.hcl; a missing file is no providers.
func LockedProviders(dir string) ([]LockedProvider, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".terraform.lock.hcl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := string(b)
	idx := lockBlock.FindAllStringSubmatchIndex(s, -1)
	out := make([]LockedProvider, 0, len(idx))
	for i, m := range idx {
		end := len(s)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		p := LockedProvider{Address: strings.ToLower(s[m[2]:m[3]])}
		for _, a := range lockAttr.FindAllStringSubmatch(s[m[1]:end], -1) {
			if a[1] == "version" {
				p.Version = a[2]
			} else {
				p.Constraints = a[2]
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// SameProvider reports whether a source as written (hashicorp/aws, or with a
// host) names the locked address. A source without a host matches the
// provider on any registry, so terraform and tofu lock files both match.
func SameProvider(source, address string) bool {
	s, a := strings.ToLower(source), strings.ToLower(address)
	if strings.Count(s, "/") == 1 {
		return strings.HasSuffix(a, "/"+s)
	}
	return s == a
}

// PruneProviders deletes the installed providers under dataDir/providers
// (dataDir is usually <root>/.terraform) that the lock file no longer lists,
// and versions of listed ones other than the locked version. It returns the
// addresses it removed entirely. Symlinks into a shared plugin cache are
// removed as links; the cache itself is never touched.
func PruneProviders(dataDir string, locked []LockedProvider) ([]string, error) {
	keep := map[string]string{}
	for _, p := range locked {
		keep[p.Address] = p.Version
	}
	base := filepath.Join(dataDir, "providers")
	hosts, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, h := range hosts {
		if !h.IsDir() {
			continue
		}
		nss, _ := os.ReadDir(filepath.Join(base, h.Name()))
		for _, ns := range nss {
			if !ns.IsDir() {
				continue
			}
			types, _ := os.ReadDir(filepath.Join(base, h.Name(), ns.Name()))
			for _, t := range types {
				addr := strings.ToLower(h.Name() + "/" + ns.Name() + "/" + t.Name())
				dir := filepath.Join(base, h.Name(), ns.Name(), t.Name())
				version, ok := keep[addr]
				if !ok {
					if err := os.RemoveAll(dir); err != nil {
						return removed, err
					}
					removed = append(removed, addr)
					continue
				}
				if version == "" {
					continue
				}
				vers, _ := os.ReadDir(dir)
				for _, v := range vers {
					if v.Name() != version {
						if err := os.RemoveAll(filepath.Join(dir, v.Name())); err != nil {
							return removed, err
						}
					}
				}
			}
			removeIfEmpty(filepath.Join(base, h.Name(), ns.Name()))
		}
		removeIfEmpty(filepath.Join(base, h.Name()))
	}
	return removed, nil
}

func removeIfEmpty(dir string) {
	if ents, err := os.ReadDir(dir); err == nil && len(ents) == 0 {
		_ = os.Remove(dir)
	}
}
