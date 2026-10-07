# Install

groundwork is one static binary for macOS and Linux, on amd64 and arm64. Windows isn't supported.

## Install script

```sh
curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | sh
```

It downloads the release archive for your OS and CPU from GitHub, **verifies its SHA-256 against the release's `checksums.txt`** and refuses to install on a mismatch, then puts `groundwork` in `~/.local/bin` (or `/usr/local/bin` if that's writable and `~/.local/bin` isn't on your PATH). It never uses `sudo`.

| Variable | |
|---|---|
| `GROUNDWORK_VERSION` | a release such as `v0.1.0` or `0.1.0` (default: the latest release; pre-releases like `v1.0.0-rc.1` only when named) |
| `GROUNDWORK_INSTALL_DIR` | where to put the binary |

To read it first: `curl -fsSLO https://raw.githubusercontent.com/rapando/groundwork/main/install.sh && less install.sh && sh install.sh`.

## Manually

Download `groundwork_<version>_<os>_<arch>.tar.gz` and `checksums.txt` from the [releases page](https://github.com/rapando/groundwork/releases), then:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
tar xzf groundwork_*_darwin_arm64.tar.gz groundwork
mv groundwork ~/.local/bin/
```

On macOS a binary downloaded with a browser is quarantined because it isn't notarized: `xattr -d com.apple.quarantine ~/.local/bin/groundwork`.

## From source

```sh
go install github.com/rapando/groundwork/cmd/groundwork@latest   # or @v0.1.0
```

Versions follow [semantic versioning](https://semver.org): a major bump (or a minor bump while on `0.x`) signals a breaking change. See [releasing](releasing.md) for how versions are chosen.

## What else you need

groundwork runs your tools; it doesn't bundle them. `groundwork doctor` lists what it found.

| Tool | For |
|---|---|
| `terraform` ≥ 1.4 or `tofu` | Terraform roots (set `terraform.binary: tofu` for OpenTofu). 1.4 is the floor (`workspace select -or-create`); tested with 1.16. |
| `ansible-core` | Ansible projects; tested with 2.21 |
| `git` | change tracking, commit recorded with each run |
| tflint, checkov, yamllint, ansible-lint | optional checks; skipped with a hint when missing |
| `sops`, `ansible-vault` | revealing encrypted values |

## Upgrading and removing

Install again to upgrade. To remove: delete the binary; each repository's `.groundwork/` folder holds its run history and can be deleted too.
