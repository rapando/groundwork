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

## Start the service

After installing, run `groundwork` in a repository: it starts the service in the background (if it isn't running), imports that repository as a project and opens it in your browser.

To start the service at login instead (launchd on macOS, systemd `--user` on Linux):

```sh
groundwork service install
groundwork service status     # running, data directory, log, console URL
```

Run `install` from a shell where `terraform` and `ansible` work: the login service keeps that shell's `PATH`. See [the service and projects](service.md) for everything else.

## What else you need

groundwork runs your tools; it doesn't bundle them. `groundwork doctor` lists what it found.

| Tool | For |
|---|---|
| `terraform` ≥ 1.4 or `tofu` | Terraform roots (set `terraform.binary: tofu` for OpenTofu). 1.4 is the floor (`workspace select -or-create`); tested with 1.16. |
| `ansible-core` | Ansible projects; tested with 2.21 |
| `git` | change tracking, commit recorded with each run |
| tflint, checkov, yamllint, ansible-lint | optional checks; skipped with a hint when missing |
| `sops`, `ansible-vault` | revealing encrypted values |

## Upgrading

Install again, then restart the service so it runs the new binary:

```sh
curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | sh
groundwork service stop && groundwork service start
```

The install script reminds you if it sees a service still running. If you moved the binary, run `groundwork service install` again so the login service points at the new location.

### From 0.1 to 0.2

0.2 turns groundwork from a per-repository server into one service for all your repositories:

- `groundwork` in a repository now starts (or reuses) the background service and returns, instead of serving in that terminal until Ctrl-C. `groundwork --foreground` keeps the old behaviour.
- Each repository you open becomes a project, and its URL moves under `/p/<id>/`. Old bookmarks land on the project list.
- Each repository's `.groundwork/` (run history, plans, rules) is used as-is: there's nothing to migrate. Run `groundwork` in each repository once, or `groundwork add <folder>…`, to bring them in.
- Scripts that started `groundwork --no-open` to drive the API should run `groundwork serve --no-open <folder>` and call `/api/p/<id>/…`. The project id is in the URL it prints.

## Removing

```sh
groundwork service uninstall          # if you installed the login service
groundwork service stop               # otherwise
rm "$(command -v groundwork)"
```

Then, if you want nothing left behind, delete the data directory (`~/Library/Application Support/groundwork` on macOS, `~/.config/groundwork` on Linux; it includes any repositories groundwork cloned for you) and each repository's `.groundwork/` folder (its run history).
