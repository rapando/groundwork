# Architecture

How groundwork is put together, as it is in the code today.

## One service, many projects

`groundwork` is a single Go binary. Run as `groundwork serve` (by hand, by the login service, or in the background by any other command) it is **the service**: one HTTP server on `127.0.0.1:7420` (opened in the browser as `localhost:7420`) holding an exclusive lock on its data directory, serving the bundled Vue SPA and a JSON API. `add`, `open`, `projects`, `remove` and `service` are thin clients of that API. `check`, `doctor`, `drift` and `init` work on the repository in the current directory in-process, without the service; `auth` edits `auth.json` directly.

Each imported repository is a **project**. A project is opened in-process with its own `api.API`, which owns a SQLite store, an event bus, a checks service, a job runner, a drift scheduler, an issue engine and a file watcher. Projects are isolated from each other: separate stores, buses and locks, and a panic in one project's background work is recovered and reported as an issue on that project only.

```
cmd/groundwork ─► internal/cli (cobra) ─► internal/service.Run
                                             │  lock, token, port, log, registry (projects.json)
                                             │  chi router: /healthz, /api/service, /api/projects, /api/p/{id}/… ─► project API
                                             │  internal/server: session + Host + Origin + CSP middleware, SSE, embedded SPA
                                             └─ per project: internal/api.API
                                                   ├ store      .groundwork/state.db (SQLite, goose migrations)
                                                   ├ events     in-process pub/sub → /api/p/{id}/events (SSE)
                                                   ├ checks     fmt/validate/tflint/checkov/yamllint/ansible-lint/syntax-check/secrets → diagnostics
                                                   ├ runner     jobs: terraform and ansible pipelines, process groups, logs
                                                   ├ drift      cron-scheduled refresh-only plans, notifications
                                                   ├ diagnostics issue engine: rules/*.yaml matched against logs and findings
                                                   └ files      repo-rooted reads/writes, fsnotify watcher, git status/diff
```

## Packages

| Package | Owns |
|---|---|
| `cli` | The command tree. Commands that need the service find it through `service.Running` (starting it if needed) and talk HTTP; `serve` calls `service.Run`; `check`, `doctor`, `drift` and `init` call the packages below directly. |
| `service` | Data directory (`Home`), the lock, the token, the rotating log, the project registry, cloning, the login units (launchd/systemd), and the top-level router that dispatches `/api/p/{id}/…` to the project's handler. |
| `auth` | Browser sign-in: the password (argon2id), passkeys (WebAuthn, via go-webauthn), and the HMAC-signed session cookie, all in `auth.json`. |
| `server` | The HTTP shell every request passes through: sign-in endpoints and session check (cookie, or the CLI's token header), Host allow-list, Origin check, security headers, request logging, panic recovery, SSE, and the embedded SPA. |
| `api` | One project's HTTP handlers and the glue between the packages below. |
| `config` | `groundwork.yaml`: schema, validation (every repo-controlled value that reaches a tool), defaults, warnings for unknown top-level keys. |
| `workspace` | Detection of Terraform roots and modules, Ansible projects and environments; the file tree with ignore rules. |
| `files` | Path containment (`Resolve`/`SafePath`), the protected-path rules, atomic writes with If-Match, the file watcher, `git status` and `git diff`. |
| `runner` | Jobs. A job is a goroutine with a context (cancelled by the user, shutdown or `run_timeout`), a per-root lock, a redactor and a log writer. Pipelines: `init → [workspace] → validate → plan → summary → approve → apply` for Terraform; `syntax → check → approve → run` for playbooks; ad-hoc, ping and facts for Ansible; refresh-only drift; force-unlock. Freshness (code, state, plan age) is re-checked under the lock before an apply. |
| `terraform`, `ansible` | Argument builders (never a shell), output parsers (JSON log lines, plan JSON, the embedded Ansible callback plugin's events), plan summaries and masked diffs. `terraform` also makes the `hclwrite` edits behind drift's *Copy into code* and *Ignore*. |
| `proc` | Process groups for subprocesses, so a cancel or timeout reaches a tool's children. |
| `redact` | Masks registered secrets (environment, sensitive variable values) and credential patterns in everything stored or shown. |
| `checks` | Runs linters per unit with a content hash cache and debounce; findings are normalised into diagnostics and stored. |
| `diagnostics` | Rules in `rules/*.yaml` turn log lines and findings into issues with explanations and actions; some issues groundwork raises itself (internal errors, orphaned processes). |
| `drift` | The drift schedule (cron) and notifications (desktop or webhook). |
| `graph` | The dependency graph (HCL analysis merged with `terraform graph`), the architecture model, and the System view: state reduced to addresses and firewall rules, host placement. Its container, nesting and server-type rules are data in `rules.yaml`. |
| `vars` | Variable precedence across tfvars and environments, Ansible variable sources, SOPS and vault reveal, the plaintext secret scan. |
| `scaffold` | Presets and templates for `init --preset` and the Scaffold screen: `aws-envs`, `gcp-envs`, `vultr`, `digitalocean`, `hetzner`, `linode`, `docker`, `onprem`. |
| `doctor` | Environment checks: tools and their version floors; AWS, Google Cloud and Azure credentials through their CLIs; Vultr, DigitalOcean, Hetzner and Linode API tokens; the Docker daemon; state backends (S3 and S3-compatible at their endpoint) and locking; SSH agent, inventory reachability, vault files, disk, git. |
| `store` | SQLite access. Migrations are embedded; opening a database integrity-checks and backs it up before migrating, and refuses one written by a newer release. |
| `events` | The pub/sub bus behind the SSE stream. |
| `safego` | Goroutine wrappers that recover panics, log them with the stack and report them. |
| `app` | Finding the repository root and bootstrapping `.groundwork/`. |
| `perf` | Tests only: timings on a generated 5,000-file repository against fixed budgets (`make perf`). |

## Data

- **Per repository** `.groundwork/` (mode 0700, self-ignored in git): `state.db` with runs, stages, approvals, diagnostics, issues, drift, host status, facts and a key/value table; `runs/<id>/` with `log.ndjson`, a masked `diff.json` and, only while a plan awaits approval, the raw `tfplan`; `rules/` for your own troubleshooting rules; `VERSION`.
- **Service data directory** (`~/Library/Application Support/groundwork`, `~/.config/groundwork`, or `$GROUNDWORK_HOME`, mode 0700): `projects.json` (with a format number), `auth.json` (sign-in methods and session key, with a format number), `token`, `server.json` (pid, port, version; never the token), `lock`, `version`, `groundwork.log` (rotated), `notify.yaml` (notification settings for all projects, which each project's `notify:` block overrides), `repos/` for clones.

What is stable across releases and how data is migrated is in [compatibility](compatibility.md).

## The event flow

The UI opens one `EventSource` per project and keeps stores current from `file.changed`, `checks.*`, `run.updated`, `log.line`, `ans.result`, `drift.updated`, `issues.updated` and `workspace.updated`; on reconnect it refetches. Publishing never blocks: a slow subscriber misses events and refetches. The CLI never uses the stream.

## The security boundary

Everything that comes from outside the binary is treated as input: HTTP requests (session, token, Host, Origin, bounded bodies, path containment), `groundwork.yaml` (validated like a request, since repositories are cloned through the UI), and tool output (parsed defensively, redacted before storage). Tools run from argument lists in their own process groups with validated values after `--`. The full model is in [security](security.md).
