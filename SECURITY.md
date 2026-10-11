# Security policy

groundwork runs `terraform` and `ansible` with your own credentials, so we treat anything that lets a web page, a repository or a config file do more than the UI allows as a vulnerability.

## Supported versions

The latest release line receives fixes. Pre-releases (`-rc.N`) are supported until the release they lead to ships.

## Reporting a vulnerability

Report privately through [GitHub security advisories](https://github.com/rapando/groundwork/security/advisories/new). Do not open a public issue.

Include the version (`groundwork version`), the OS, and steps to reproduce. You should hear back within a week. Once a fix is released the advisory is published with credit, unless you ask otherwise.

## What is in scope

- The security model described in [docs/security.md](docs/security.md): loopback-only binding, session token and cookie, Host and Origin checks, path containment, "no shell", redaction of logs and plans.
- Anything in `groundwork.yaml`, a repository checked out through the UI, or a tool's output that can execute code or exfiltrate values the UI would hide.
- The install script and the integrity of release artifacts.

Out of scope: issues that require an attacker who already runs code as your user, and the behaviour of terraform, ansible and the cloud CLIs themselves.
