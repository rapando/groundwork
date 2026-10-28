# Install

groundwork is one static binary for macOS and Linux, on amd64 and arm64. Windows isn't supported. The same binary is the CLI and the background service that hosts your projects.

In short:

```sh
curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | sh
groundwork service install    # optional: start the service at login
cd ~/code/your-infra-repo && groundwork
```

## Install script

```sh
curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | sh
```

It downloads the release archive for your OS and CPU from GitHub, **verifies its SHA-256 against the release's `checksums.txt`** and refuses to install on a mismatch, then puts `groundwork` in `~/.local/bin` (or `/usr/local/bin` if that's writable and `~/.local/bin` isn't on your PATH). It never uses `sudo`. On macOS it also clears the quarantine flag, and if a groundwork service is still running it reminds you to restart it.

`checksums.txt` is signed with [cosign](https://docs.sigstore.dev/cosign/installation/) (keyless: the certificate names this repository's release workflow). When `cosign` is on your PATH the script verifies the signature before trusting the checksums and says so; otherwise it prints `Signature not verified (cosign not installed)` and relies on the checksum alone. A signature that is published but doesn't verify always stops the install; set `GROUNDWORK_REQUIRE_SIGNATURE=1` to also refuse when cosign or the signature is missing. Releases before 0.5.1 are not signed. Verifying needs cosign 2.4 or later (the signature is a Sigstore bundle).

| Variable | |
|---|---|
| `GROUNDWORK_VERSION` | a release such as `v1.0.0` or `1.0.0` (default: the latest release; pre-releases like `v1.1.0-rc.1` only when named) |
| `GROUNDWORK_INSTALL_DIR` | where to put the binary |
| `GROUNDWORK_DOWNLOAD_BASE` | a mirror of the release downloads, for air-gapped installs |
| `GROUNDWORK_REQUIRE_SIGNATURE` | `1` to refuse to install without a verified signature |

To read it first: `curl -fsSLO https://raw.githubusercontent.com/rapando/groundwork/main/install.sh && less install.sh && sh install.sh`.

## Manually

Download `groundwork_<version>_<os>_<arch>.tar.gz` and `checksums.txt` from the [releases page](https://github.com/rapando/groundwork/releases), then:

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/rapando/groundwork/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
tar xzf groundwork_*_darwin_arm64.tar.gz groundwork
mv groundwork ~/.local/bin/
```

Each archive also has a software bill of materials next to it (`groundwork_<version>_<os>_<arch>.tar.gz.sbom.spdx.json`).

On macOS a binary downloaded with a browser is quarantined because it isn't notarized: `xattr -d com.apple.quarantine ~/.local/bin/groundwork`.

## From source

```sh
go install github.com/rapando/groundwork/v2/cmd/groundwork@latest   # or @v2.0.0
```

This needs Go 1.27 or later. Versions follow [semantic versioning](https://semver.org): only a major bump signals a breaking change; what counts as one is in [compatibility](compatibility.md). See [releasing](releasing.md) for how versions are chosen.

## Start the service

After installing, run `groundwork` in a repository: it starts the service in the background (if it isn't running), imports that repository as a project and opens it in your browser.

To start the service at login instead (launchd on macOS, systemd `--user` on Linux):

```sh
groundwork service install
groundwork service status     # running, data directory, log, console URL
```

Run `install` from a shell where `terraform` and `ansible` work: the login service keeps that shell's `PATH`. See [the service and projects](service.md) for everything else.

## What else you need

groundwork runs your tools; it doesn't bundle them. `groundwork doctor` lists what it found and warns when a tool is older than the floor below (nothing is refused: older releases are simply untested).

| Tool | For |
|---|---|
| `terraform` ≥ 1.4 or `tofu` ≥ 1.6 | Terraform roots (set `terraform.binary: tofu` for OpenTofu). 1.4 adds `workspace select -or-create`; tested with 1.16. |
| `ansible-core` ≥ 2.14 | Ansible projects; tested with 2.21 |
| `git` | change tracking, commit recorded with each run |
| tflint, checkov, yamllint, ansible-lint | optional checks; skipped with a hint when missing |
| `sops`, `ansible-vault` | revealing encrypted values |
| `aws`, `docker` | optional: Doctor uses them to check S3 state buckets and lock tables, and the Docker daemon |

## Upgrading

Install again, then restart the service so it runs the new binary:

```sh
curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | sh
groundwork service restart
```

The install script reminds you if it sees a service still running, and every CLI command warns while the service runs a different version than the command (`groundwork service status` shows both). If you moved the binary, run `groundwork service install` again so the login service points at the new location.

What an upgrade touches, and how it is protected, is in [compatibility](compatibility.md): each project's `state.db` is migrated when the project is next opened (a copy is kept as `state.db.bak-<schema>` first), the service data directory and `.groundwork/` record the version that last wrote them, and a newer release's data is refused by an older binary with a clear message rather than misread.

### From 0.1 to 0.2

If you still run groundwork the 0.1 way, one server per repository in your terminal, note what changed when 0.2 made it one service for all your repositories:

- `groundwork` in a repository now starts (or reuses) the background service and returns, instead of serving in that terminal until Ctrl-C. `groundwork --foreground` keeps the old behaviour.
- Each repository you open becomes a project, and its URL moves under `/p/<id>/`. Old bookmarks land on the project list.
- Each repository's `.groundwork/` (run history, plans, rules) is kept. Run `groundwork` in each repository once, or `groundwork add <folder>…`, to bring them in.
- Scripts that drove the HTTP API should use the CLI instead (`groundwork check --json`, `groundwork doctor --json`, `groundwork projects`): the API is private to the bundled UI.

## Removing

```sh
groundwork service uninstall          # if you installed the login service
groundwork service stop               # otherwise
rm "$(command -v groundwork)"
```

Then, if you want nothing left behind, delete the data directory (`~/Library/Application Support/groundwork` on macOS, `~/.config/groundwork` on Linux; it includes any repositories groundwork cloned for you) and each repository's `.groundwork/` folder (its run history).
