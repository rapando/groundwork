# groundwork — Implementation Plan

> Working name: **groundwork** (binary `groundwork`). The repo folder is `iacg`. If the product takes that name instead, it is a find-and-replace.
> UI design: see [`design/`](design/README.md) and the live canvas linked there.

---

## 1. What we're building

A single, self-contained Go binary. You run it inside a git repository and it:

1. **Detects** Terraform and Ansible code. This works in a dedicated IaC repo and also in an application repo that keeps its infrastructure in a subfolder (`deploy/terraform`, `ops/ansible`, …).
2. **Scaffolds** a well-structured IaC layout when the repo is empty, or adds only what's missing when the repo isn't.
3. **Serves a local web UI** on `127.0.0.1` and opens it in the browser. All work happens there:
   - edit code and see it visualised
   - verify it (fmt, validate, lint, policy)
   - run plans, applies and playbooks with live logs
   - review and approve plans
   - browse inventory, variables and secrets
   - see drift and resource graphs
   - troubleshoot failures with guided fixes

It is **free, self-hosted and local-first**. There are no accounts and no server component. Groundwork does not talk to the network except through the Terraform, Ansible and cloud tools the user already runs.

### Goals

- Zero-config start: `cd repo && groundwork`.
- One binary, no runtime dependencies besides the IaC tools themselves (terraform/tofu, ansible, optional linters).
- Never surprise the user. Nothing touches real infrastructure without an explicit, reviewed approval. Destructive actions need a typed confirmation.
- The repo is the source of truth. Groundwork's own state lives in `.groundwork/`, which is git-ignored by default. The only file it adds to the repo is `groundwork.yaml`.
- Works on macOS and Linux first. Windows is supported for Terraform; Ansible on Windows requires WSL, because Ansible itself has no native Windows control node.

### Non-goals (v1)

- Multi-user server mode, auth, RBAC.
- Remote execution or agents.
- Replacing Terraform Cloud, Atlantis or CI. Groundwork shows CI workflows but doesn't run them.
- Supporting Pulumi, CDK, Helm or Packer. The design leaves room for this later (see §17).

---

## 2. Product principles → engineering rules

| Principle | Rule in code |
|---|---|
| Local only | HTTP server binds `127.0.0.1` only; a per-session token is required on every request (§12). |
| Read-only by default | Only the job runner can execute mutating commands (`apply`, `force-unlock`, playbooks without `--check`). Every mutating job needs an `approval` record. |
| Plans are contracts | `apply` always uses a saved plan file (`-out`). Approval is tied to the plan's SHA-256 and the state serial; if either changes, the plan is invalidated. |
| Secrets stay in memory | Decrypted values are never written to disk or logs, and never sent to the browser unless the user clicks "Reveal". Log redaction runs on all job output (§8.9). |
| Repo layout is respected | In `embedded` mode, groundwork writes only `groundwork.yaml` plus optional lint configs the user ticks. |
| Explain, don't just fail | Every error surface passes through the diagnostics normaliser and the troubleshooting rules engine (§8.10). |

---

## 3. Architecture

```
┌──────────────────────────── groundwork (single Go binary) ─────────────────────────────┐
│                                                                                         │
│  cmd/groundwork  ──►  CLI (cobra)  ──►  App  ─────────────────────────────────────────┐ │
│                                          │                                            │ │
│   ┌────────────── HTTP server (net/http + chi) ──────────────┐                        │ │
│   │  /api/*  REST JSON        /api/events  SSE stream         │◄── embedded SPA (Vue) │ │
│   │  auth middleware (token, Host, Origin)                    │    via embed.FS       │ │
│   └───────────────────────────────┬───────────────────────────┘                        │ │
│                                   │                                                    │ │
│   ┌──────────── Core services ────┴──────────────────────────────────────────────────┐ │ │
│   │ workspace (detect, model, config)   files (read/write, fsnotify watcher)        │ │ │
│   │ checks (fmt/validate/tflint/...)    diagnostics (normalise + rules engine)      │ │ │
│   │ runner (job queue, process mgmt)    terraform adapter    ansible adapter        │ │ │
│   │ graph (deps + architecture)         vars & secrets       drift                  │ │ │
│   │ scaffold (templates)                doctor               onboarding             │ │ │
│   └───────────────────────────────┬──────────────────────────────────────────────────┘ │ │
│                                   │                                                    │ │
│   ┌──────── Storage (.groundwork/) ┴──────┐      ┌──── External tools (os/exec) ────┐  │ │
│   │ state.db (SQLite, pure Go)            │      │ terraform | tofu, ansible-*,     │  │ │
│   │ runs/<id>/{log.ndjson,tfplan,plan.json}│      │ tflint, checkov, ansible-lint,   │  │ │
│   └───────────────────────────────────────┘      │ yamllint, sops, git              │  │ │
│                                                  └──────────────────────────────────┘  │ │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

**Event bus.** One in-process pub/sub (`internal/events`) fans out these events to SSE clients:
- file changes
- check results
- job lifecycle and log lines
- drift updates

The UI subscribes once and updates its stores from the stream. This avoids polling.

---

## 4. Tech stack

### Backend (Go ≥ 1.23, `CGO_ENABLED=0`)

| Concern | Choice | Why |
|---|---|---|
| CLI | `spf13/cobra` | Standard; subcommands + flags. |
| HTTP | `net/http` + `go-chi/chi/v5` | Small, idiomatic; middleware chain. |
| Live updates | Server-Sent Events | One-way stream is all we need. Simpler than WebSockets, and works through the same auth middleware. |
| Storage | `modernc.org/sqlite` | Pure Go (no CGO), so cross-compiling stays trivial. |
| Migrations | `pressly/goose` (embedded SQL) | |
| HCL parsing | `hashicorp/hcl/v2`, `hashicorp/terraform-config-inspect` | Find variables, outputs, modules, providers and resources without running terraform. |
| Terraform JSON | `hashicorp/terraform-json` | Typed structs for `show -json`, `validate -json`, provider schemas. |
| YAML | `gopkg.in/yaml.v3` (keeps node positions) | Line numbers for diagnostics and the "jump to definition" links. |
| File watching | `fsnotify/fsnotify` + debounce | Re-run checks on save. |
| Gitignore | `go-git/go-git/v5/plumbing/format/gitignore` | Skip ignored paths during detection. |
| SOPS | `getsops/sops/v3/decrypt` | Decrypt in-process, so the user doesn't need `sops` on PATH. |
| Open browser | `pkg/browser` | |
| Logging | `log/slog` | |
| Release | GoReleaser | darwin/linux/windows × amd64/arm64, Homebrew tap, checksums, SBOM. |

### Frontend (built at release time, embedded with `embed.FS`)

| Concern | Choice |
|---|---|
| Framework | Vue 3 + TypeScript + Vite |
| State | Pinia (one store per domain: workspace, runs, checks, graph, vars…) |
| Routing | vue-router (history mode; server falls back to `index.html`) |
| Code editor | CodeMirror 6 (HCL via `@codemirror/legacy-modes` or a Lezer grammar, YAML, JSON). Lint gutter is fed from the diagnostics API. |
| Graph rendering | Vue Flow for interaction + `elkjs` for layout (layered for dependencies, nested for architecture) |
| Styling | Plain CSS with custom properties from the design tokens (`design/README.md`). No UI framework, to match the design exactly. |
| Tests | Vitest (unit), Playwright (e2e against a fixture repo) |

> The design uses IBM Plex Sans and JetBrains Mono. Bundle them as woff2 in the SPA; don't load them from Google Fonts. The tool must work offline.

---

## 5. Repository layout

```
iacg/
├── cmd/groundwork/main.go
├── internal/
│   ├── app/            # wiring, lifecycle, config load
│   ├── cli/            # cobra commands
│   ├── server/         # router, middleware (auth, logging, recover), SSE hub, static SPA
│   ├── api/            # HTTP handlers, request/response DTOs
│   ├── events/         # in-process pub/sub
│   ├── config/         # groundwork.yaml schema, load/save, validation, defaults
│   ├── workspace/      # detection + domain model (Roots, Modules, AnsibleProjects, Envs)
│   ├── files/          # safe FS access (repo-rooted), watcher, git status
│   ├── runner/         # job queue, process groups, cancellation, log capture
│   ├── terraform/      # adapter: init/validate/plan/show/apply/graph/state/unlock
│   ├── ansible/        # adapter: inventory, playbook, check/diff, syntax, vault
│   │   └── callback/   # embedded Python callback plugin (NDJSON events)
│   ├── checks/         # fmt, validate, tflint, checkov, ansible-lint, yamllint → Diagnostic
│   ├── diagnostics/    # normalised model + troubleshooting rules engine
│   │   └── rules/*.yaml
│   ├── drift/          # refresh-only plans, scheduling
│   ├── graph/          # dependency graph (DOT + plan JSON) and architecture model
│   ├── vars/           # tfvars resolution, ansible var precedence, secrets, plaintext scan
│   ├── scaffold/       # presets + templates (embed.FS), dry-run preview, zip
│   ├── doctor/         # environment health checks
│   ├── onboarding/     # first-run state, checklist derivation
│   ├── store/          # SQLite repo layer + migrations
│   └── redact/         # secret redaction for logs
├── web/                # Vue SPA (vite project); build output → internal/server/dist
├── testdata/
│   ├── repos/          # fixture repos: iac-only, embedded, empty, broken, workspaces
│   └── golden/         # expected parser outputs
├── design/             # UI design sources (this snapshot)
├── docs/
├── .goreleaser.yaml
├── Makefile
└── IMPLEMENTATION_PLAN.md
```

---

## 6. CLI surface

```
groundwork                 # detect → (setup if needed) → serve → open browser
groundwork serve [--port 7420] [--no-open] [--host 127.0.0.1]
groundwork init  [--preset aws-envs|gcp-envs|hetzner|onprem] [--detect] [--dry-run] [--yes]
groundwork check [--json]  # run all checks once, exit 1 on errors (CI-friendly)
groundwork doctor          # environment health, prints table
groundwork drift [--env prod]
groundwork version
```

- **Default command.** If `groundwork.yaml` is missing, it starts the server in *setup mode*:
  - IaC found → UI opens `/setup/detect`.
  - Repo empty → UI opens `/setup/scaffold`.
- **Port.** Prefer 7420. If it's busy, pick a free port. Print the URL with its token: `http://127.0.0.1:7420/?t=…`.
- **Same repo twice.** A second `groundwork` in the same repo finds the running instance via `.groundwork/server.json` (pid, port, token) and opens the browser to it instead of starting another.

---

## 7. Configuration — `groundwork.yaml`

```yaml
version: 1
mode: standalone            # standalone (dedicated IaC repo) | embedded (app repo)
terraform:
  binary: terraform         # or "tofu"
  roots:
    - path: terraform/envs/dev
      env: dev
    - path: terraform/envs/prod
      env: prod
      approval: required    # required | none (default: required for prod-like names)
    - path: deploy/terraform          # workspace-style root
      envs:
        dev:  { workspace: dev,  var_files: [env/dev.tfvars] }
        prod: { workspace: prod, var_files: [env/prod.tfvars], approval: required }
  modules: [terraform/modules/*]
ansible:
  projects:
    - path: ansible
      config: ansible.cfg
      inventories:
        dev:  inventory/dev.yml
        prod: inventory/prod.yml
      vault_password_file: ~/.config/groundwork/vault-pass   # optional, never committed
checks:
  enabled: [fmt, validate, tflint, ansible-lint, yamllint]   # + checkov, syntax-check
  on_save: true
drift:
  schedule: "0 */6 * * *"   # optional; only while the server is running
  notify: desktop           # desktop | webhook | none
  webhook: ""
ignore: [vendor/, node_modules/, "**/.terraform/"]
```

- **Schema and validation.** The schema is a Go struct with `yaml` tags. It is validated on load, and errors carry line and column (`yaml.v3` Node). A JSON Schema is generated and served for editor autocomplete.
- **Environments are first-class.** An *Environment* is the join of Terraform roots/workspaces and Ansible inventories that share a name. The UI uses it everywhere: sidebar, Overview cards, Inventory tabs, Variables columns.

---

## 8. Core subsystems

### 8.1 Workspace detection (`internal/workspace`)

**Input.** The repo root: the first ancestor containing `.git`, or the CWD if there isn't one.

**Walk.** The walk respects `.gitignore`, `ignore:` in the config, and hard-coded skips (`.git`, `.terraform`, `node_modules`, `vendor`, `.venv`). Stop after 50k files and warn.

**Terraform signals** (scored per directory):

| Signal | Weight |
|---|---|
| `backend "…"` or `cloud {}` block | root +5 |
| `provider "…"` block | root +3 |
| `required_providers` | +2 |
| `*.tfvars` present | root +2 |
| referenced as `source = "./…"` by another dir | **module** (overrides root) |
| only `variable`/`output`/`resource`, no backend/provider | module +3 |

**Ansible signals:**

| Signal | Meaning |
|---|---|
| `ansible.cfg` | project root (strong) |
| YAML list items with `hosts:` + (`tasks:`\|`roles:`\|`import_playbook:`) | playbook |
| `roles/<name>/tasks/main.yml` | role |
| `inventory/`, `hosts`, `hosts.ini`, `*.yml` with `all:`/`children:` | inventory |
| `group_vars/`, `host_vars/` | vars dirs (attach to nearest project) |

**CI signals (read-only).** `.github/workflows/*.yml`, `.gitlab-ci.yml`. Look for `terraform`, `tofu` and `ansible-playbook` in `run:` steps and record their `working-directory`.

**Environment inference:**
- Directory names under `envs/` or `environments/`.
- tfvars basenames.
- `terraform workspace list`, but only when `.terraform/` already exists. Never run `init` during detection.
- Inventory file names and top-level groups.
- Match across tools by normalised name (`production` ≡ `prod`, `stg` ≡ `staging`).

**Mode.** `standalone` if more than 60% of tracked files are IaC or YAML; otherwise `embedded`.

**Output.** A `DetectionReport`: each finding carries its evidence (file and line), which the Detect screen shows. `config.FromReport()` produces the `groundwork.yaml` preview.

**Tests.** Fixture repos under `testdata/repos/*`, with a golden `DetectionReport` JSON for each.

### 8.2 Files (`internal/files`)

- `SafePath(rel)`: cleans the path and rejects absolute paths, `..` escapes and symlinks that resolve outside the repo. Every read and write goes through it.
- **Read.** Returns content, mtime and SHA-256.
- **Write.** Takes an `If-Match: <sha>` precondition and returns 409 on conflict. This guards against concurrent edits from the user's IDE.
- **Watcher.** fsnotify on the managed paths, with a 300 ms debounce. Each change emits `file.changed`, which triggers the incremental check run (§8.6) and invalidates the graph cache.
- **Git status.** Shells out to `git status --porcelain=v2 -z` for the "uncommitted" panel and `git diff` for the diff view. Using the git CLI keeps behaviour identical to the user's own git.

### 8.3 Runner (`internal/runner`)

**Job model:**
```go
type Job struct {
  ID        int64
  Kind      string   // tf.init, tf.validate, tf.plan, tf.apply, tf.unlock, tf.drift,
                     // ans.playbook, ans.check, ans.adhoc, ans.ping, ans.facts, check.*
  Target    Target   // root/env or ansible project/env/limit
  Argv      []string // exact command (shown in UI "Copy command")
  Env       map[string]string // redacted in storage
  Stages    []Stage  // e.g. init → validate → plan → approve → apply → drift-check
  Status    Status   // queued, running, waiting_approval, succeeded, failed, cancelled
  Approval  *Approval
  StartedAt, EndedAt time.Time
  Commit    string   // git HEAD at start
}
```

**Concurrency.** One active job per **lock key**. For Terraform, the key is root + workspace. For Ansible, it is project + env. Other jobs queue. Read-only checks run on a separate pool and never take a lock.

**Process management:**
- `exec.CommandContext` with its own process group (`Setpgid`). On cancel, send SIGINT to the group, then SIGKILL after 15 s. This gives Terraform a chance to release the state lock; the Troubleshoot screen exists because that sometimes fails.
- On Windows, use job objects.

**Output capture:**
- stdout and stderr are line-scanned, then redacted (§8.9).
- Each line is timestamped, given a stage and a level by the adapter's classifier, and appended to `runs/<id>/log.ndjson`.
- Lines are published on the event bus.
- Size cap: 50 MB per run. Beyond that, keep the head and the tail.

**Environment.** Inherits the user's environment (AWS_PROFILE etc.), plus `TF_IN_AUTOMATION=1`, `TF_INPUT=0` and `NO_COLOR=1`. Ansible gets the callback plugin variables (§8.5).

**Crash safety.** On startup, mark any `running` jobs as `failed (interrupted)`. If any of them was a Terraform apply, open a "possible stale lock" issue for that root.

### 8.4 Terraform adapter (`internal/terraform`)

| Operation | Command | Parsed via |
|---|---|---|
| Version / binary | `terraform version -json` | JSON |
| Init | `init -input=false -no-color` (`-upgrade` when asked) | log classifier |
| Validate | `validate -json` | `tfjson.ValidateOutput` → Diagnostics |
| Format | `fmt -check -diff -recursive` / `fmt -write` | file list + diff |
| Plan | `plan -input=false -out=.groundwork/runs/<id>/tfplan -detailed-exitcode [-var-file…]` | exit code 0/1/2 |
| Plan JSON | `show -json tfplan` | `tfjson.Plan` → PlanSummary + per-resource diffs |
| Apply | `apply -input=false tfplan` | per-resource progress from log lines (`: Creating…`, `: Creation complete`) |
| Drift | `plan -refresh-only -json -out=…` | changed attributes per resource |
| Graph | `graph -type=plan` (DOT) | `gonum/graph/encoding/dot` or a small parser |
| State | `state list`, `state show -json`… (via `show -json` on state) | for the Graph inspector |
| Unlock | `force-unlock -force <id>` | requires typed confirmation |
| Workspaces | `workspace list/select` | |
| Providers schema | `providers schema -json` (cached by lock-file hash) | attribute docs and `ForceNew` hints |

**Plan review data (Plan screen):**
- Counts for add, change, replace and destroy.
- Replacements are detected from `change.actions == ["delete","create"]` or `["create","delete"]`, plus `replace_paths`. These drive "forces replacement" on the specific attribute.
- **Danger rules** (configurable): replacing or destroying a resource whose type matches `*_db_*`, `*_rds_*`, `*_bucket*`, `*_volume*` or `*_disk*`, or one that has `prevent_destroy` elsewhere. These show the red banner and the "I understand" checkbox.
- **Staleness.** A plan is invalid if:
  - the current state serial (`show -json` → `serial`, or a backend read) ≠ the plan's prior state serial, or
  - the plan is older than a configurable TTL (default 1 h), or
  - the git HEAD changed in that root.

**OpenTofu.** Use the same adapter with `binary: tofu`. Feature-detect differences from the version output.

### 8.5 Ansible adapter (`internal/ansible`)

**Inventory.** Run `ansible-inventory -i <inv> --list --export` to get hosts, groups and vars. Run `ansible-inventory --host <h>` for a host's resolved vars.

**Variable provenance** (Inventory screen, "winner first" table). Ansible doesn't report where a value came from, so groundwork resolves it independently: role defaults → inventory group_vars → playbook group_vars → host_vars (simplified precedence ladder, documented). It then cross-checks the winning value against `ansible-inventory --host`. If they disagree, show a "can't determine source" badge rather than guessing.

**Playbook runs:**
- Run `ansible-playbook` with an **embedded callback plugin** (≈150 lines of Python). It is written to `.groundwork/ansible/callback/` and enabled via `ANSIBLE_CALLBACKS_ENABLED=groundwork` and `ANSIBLE_CALLBACK_PLUGINS=…`.
- The plugin emits NDJSON events on fd 3: play start, task start, per-host result (ok/changed/failed/skipped/unreachable, diff), and stats. Normal human output still goes to stdout for the log pane.
- Go reads fd 3 to build the per-host and per-task grid and the "last play" column in Inventory.

**Modes:**
- `--check --diff` is the dry run. It's the default for prod unless approved.
- `--syntax-check` runs as part of checks.
- `--limit`, `--tags` and `--skip-tags` come from the UI.
- Ad-hoc commands: `ansible <pattern> -m <module> -a …`. Mutating modules require confirmation; `ping`, `setup` and `command` with `--check` do not.

**Ping and facts.** `ansible all -m ping -o` and `-m setup` (cached in SQLite with a timestamp, shown as "gathered 18m ago").

**Vault.** Shell out to `ansible-vault view` with `--vault-id` / `--vault-password-file`. Detect "Decryption failed" and map it to the troubleshooting rule.

**Windows.** Detect the missing `ansible` binary and show the doctor hint "Ansible needs WSL on Windows".

### 8.6 Checks / verification (`internal/checks`)

**Unified model:**
```go
type Diagnostic struct {
  Tool     string   // validate, tflint, checkov, ansible-lint, yamllint, fmt, syntax-check
  Severity string   // error, warning, info
  Code     string   // rule id, e.g. terraform_unused_declarations, no-changed-when
  Message  string
  File     string; Line, Col, EndLine, EndCol int
  Fix      *QuickFix // optional machine-applicable edit (e.g. did-you-mean)
  RuleRef  string    // troubleshooting rule id if matched
}
```

| Tool | Invocation | Parser |
|---|---|---|
| terraform fmt | `fmt -check -recursive -list=true` | file list → one diagnostic per file, quick fix = `fmt -write` |
| terraform validate | `validate -json` (needs init; uses `-backend=false` init in a temp data dir to avoid touching state) | tfjson diagnostics with ranges |
| tflint | `tflint --format=json --chdir=<root>` | JSON issues |
| checkov | `checkov -d <path> -o json --quiet` | failed_checks |
| ansible-lint | `ansible-lint -f json` (or `codeclimate`) | JSON |
| yamllint | `yamllint -f parsable` | regex |
| syntax-check | `ansible-playbook --syntax-check` | stderr classifier |

**Incremental runs.** On `file.changed`, map the file to its owning root or project and re-run only the checks for that unit. Debounce per unit. Cache results keyed by content hash.

**"Did you mean".** When validate reports an unsupported argument, look up the resource type's schema (`providers schema -json`). Suggest the closest attribute by Levenshtein distance; that suggestion becomes the inline quick fix button in the design.

**Missing tools** degrade to `skipped`, with an install hint (Doctor).

**CI mode.** `groundwork check --json` runs the same pipeline once and exits non-zero on errors.

### 8.7 Drift (`internal/drift`)

- On demand ("Detect drift"), or on the optional cron while the server runs (`robfig/cron/v3`).
- Runs `plan -refresh-only` per root/env as a read-only job (takes the root lock, no approval).
- Stores the drifted resources and attributes (code vs actual) for the Graph inspector table and the Overview env status.
- **Actions** (Graph screen):
  - *Copy change into code*: generates an HCL patch from the actual values for simple attributes, shown as a diff before writing.
  - *Revert with apply*: creates a normal plan job.
  - *Ignore attribute*: suggests a `lifecycle { ignore_changes = [...] }` patch.

### 8.8 Graph & visualisation (`internal/graph` + web)

There are two views.

**Dependencies** (Graph screen):
- Nodes and edges from `terraform graph -type=plan` (DOT) when the root is initialised. Otherwise from static analysis: HCL expression traversals via `hcl.Traversal` of references like `aws_vpc.main.id`.
- Collapse `[count]` / `[for_each]` instances into one node with ×N.
- Overlay state from the latest plan (create/update/replace/delete) and drift (dashed amber).
- Layout: elkjs `layered`, left→right.

**Architecture** (Code screen → Visualize). This answers "what does this module build", using containment rather than arrows:
- Containment is derived from reference attributes. A provider-specific rule table maps attributes to parent types: `vpc_id → aws_vpc`, `subnet_id`/`subnet_ids → aws_subnet`, `network_interface → …`, plus GCP/Azure/Hetzner equivalents.
- Unknown resources fall back to the dependency view.
- Built from the **parsed HCL in the editor buffer**, debounced 400 ms, so it updates as you type without saving. This is pure static analysis: no terraform calls, no credentials.
- **Cursor sync.** The editor cursor's enclosing block id is highlighted (green outline). Clicking a box moves the cursor to its definition.
- Error overlay: blocks with diagnostics get a red dashed border.
- Layout: elkjs with hierarchy (`elk.hierarchyHandling: INCLUDE_CHILDREN`).
- The env selector ("as used by envs/staging") substitutes module inputs from that env's tfvars, so counts like `×3` are concrete.

**Export.** SVG and PNG, rendered client-side.

### 8.9 Variables & secrets (`internal/vars`, `internal/redact`)

**Terraform variable matrix** (Variables screen):
- Declarations come from `terraform-config-inspect`: name, type, default, sensitive, validation, description and position.
- Values per env follow Terraform's precedence:
  1. defaults
  2. `terraform.tfvars`
  3. `*.auto.tfvars` (lexical order)
  4. `-var-file`s from config
  5. `TF_VAR_*` (shown as "from environment", value hidden)
- Each cell records its winning source file and line.
- Flags: *missing* (required with no value), *unused* (declared but never referenced, cross-checked with tflint), *differs* (filter toggle).
- Editing a cell writes to the chosen tfvars file with `hclwrite`, which preserves comments and formatting. Validation re-runs afterwards.

**Ansible variables.** Same matrix, with groups as rows and envs as columns, using the provenance logic from §8.5.

**Secrets:**
- *SOPS* (`*.enc.yaml|json`, `.sops.yaml` rules): decrypt via the Go library using the user's age, PGP or KMS keys. The status column shows "can decrypt" or the reason it can't.
- *ansible-vault*: detect `$ANSIBLE_VAULT;` headers, decrypt via the CLI.
- *Environment-sourced* credentials (AWS profile and similar) are listed, never read.
- **Reveal** is an explicit POST that returns the value once. It isn't cached in the store, and it's logged as an audit event (name only, never the value).

**Plaintext scan.** Run on changed files: gitleaks-style regexes plus an entropy check, scoped to IaC files. "Move to SOPS" creates or appends the encrypted file, replaces the literal with a `data`/var reference, and shows the diff before writing.

**Redaction.** All job output passes through `redact.Writer`, which replaces:
- known secret values loaded this session,
- `sensitive` variable values,
- common token patterns (AWS keys, bearer tokens, private keys)

with `••••`. Plans: `sensitive` values are already masked by Terraform; groundwork never renders `(sensitive value)` contents.

### 8.10 Troubleshooting & Doctor (`internal/diagnostics`, `internal/doctor`)

**Rules engine.** Rules are YAML files embedded in the binary. Users can override or extend them with `.groundwork/rules/*.yaml`.

```yaml
id: tf-state-lock-stale
match:
  any:
    - log: "Error acquiring the state lock"
    - diagnostic: { tool: terraform, summary: "Error acquiring the state lock" }
extract:
  lock_id: 'ID:\s+([0-9a-f-]{36})'
  who: 'Who:\s+(.+)'
  created: 'Created:\s+(.+)'
title: "A crashed run left the state locked"
explain: >
  Run {{ .Run.ID }} was interrupted during {{ .Run.Kind }} and exited before releasing the lock.
checks:                         # automated pre-checks shown as ✓ steps
  - no_local_process: terraform
  - no_running_job_on: "{{ .Target }}"
actions:
  - kind: command
    label: Force unlock
    argv: ["terraform", "-chdir={{ .Root }}", "force-unlock", "-force", "{{ .lock_id }}"]
    confirm: typed:{{ .Target }}
    mutating: true
  - kind: plan
    label: "Plan {{ .Target }}"
```

**Initial rule set** (≈20 rules):
- stale state lock
- provider checksum mismatch / lock file
- unsupported argument (did-you-mean)
- missing required variable
- backend access denied (S3 403 / KMS)
- expired AWS SSO session
- provider version constraint conflict
- module source not found
- ansible UNREACHABLE (ssh timeout, host key changed, auth failed)
- vault decryption failed
- python interpreter discovery warning
- `become` password required
- yamllint indentation
- ansible-lint `no-changed-when` / `name[casing]` explanations

**Issue lifecycle.** Matched diagnostics and log events become *Issues*: open → resolved, when the source condition disappears or the user marks it resolved. These feed Overview "Needs attention" and the Troubleshoot list ("14 resolved this week").

**Doctor checks:**
- tool versions and PATH
- cloud credentials and their expiry (`aws sts get-caller-identity`, `gcloud auth print-access-token --dry-run`…)
- state backend reachability (backend-specific HEAD/list)
- lock table
- SSH agent keys (`ssh-add -l`)
- inventory reachability (ping)
- vault password file present
- disk space in `.groundwork/`

Each check returns ok/warn/fail/missing plus a hint.

### 8.11 Scaffolding (`internal/scaffold`)

- **Presets** are template trees in `embed.FS` (`text/template` with a small func map), parameterised by the setup form: provider, backend, bucket, region, envs, layout (dir-per-env vs workspaces), ansible inventory source, starter roles, checks, CI.
- **Dry run first.** The preview tree on the Scaffold screen comes from a render-to-memory pass. "Create files" writes it; existing files are never overwritten unless the user confirms each one. "Download as zip" streams the same render.
- **Embedded mode.** Writes only `groundwork.yaml`, the optional lint configs, and a `.gitignore` line for `.groundwork/`.
- **Optional extras.** An initial git commit (`git add` + `git commit -m "chore: scaffold with groundwork"`). Installing missing tools only *prints* the commands (brew/pipx/apt); groundwork doesn't run package managers itself.
- **Inventory from Terraform outputs.** Generate `inventory/tf_outputs.yml`, populated by a groundwork-managed `ansible.cfg` inventory script that reads `terraform output -json`. The same mechanism powers "source: terraform outputs" in Inventory.

### 8.12 Storage (`internal/store`)

`.groundwork/state.db` (SQLite, WAL mode). Tables:

```
runs(id, kind, target_json, argv_json, status, stage, commit, started_at, ended_at, exit_code, summary_json)
run_stages(run_id, name, status, started_at, ended_at, detail)
approvals(run_id, plan_sha256, state_serial, approved_by, approved_at, confirm_text)
diagnostics(id, unit, tool, severity, code, file, line, col, message, fix_json, content_hash, seen_at)
issues(id, rule_id, target, status, first_seen, last_seen, context_json, resolved_at)
drift(id, root, env, address, attr_path, code_value, actual_value, detected_at)
facts(host, env, json, gathered_at)
kv(key, value)            -- onboarding state, settings, server info
schema_migrations(...)
```

- Logs and plan artefacts live as files in `.groundwork/runs/<id>/`.
- **Retention:** keep 500 runs or 30 days, configurable. A janitor runs on startup.
- `approved_by` is the OS username. There are no accounts.

### 8.13 Onboarding (`internal/onboarding`)

- `kv['onboarding.tour_completed']` and `kv['onboarding.checklist_hidden']` are stored per repo. A global `~/.config/groundwork/settings.json` holds `tips: false` for people who never want the tour.
- **Checklist items are derived from real data**, not ticked by hand:
  1. Repo scanned: the detection report exists.
  2. Checks ran: a check run exists.
  3. First plan: any `tf.plan` job exists.
  4. Visualize used: a UI event `visualize.opened`.
  5. Doctor: any doctor run exists.
- Each tour step highlights a stable `data-tour="nav|envs|checks|issues|header"` anchor. The UI positions the tip with floating-ui rather than the fixed offsets in the mockup.

---

## 9. HTTP API

All routes are under `/api`, JSON, and require the session token (§12).

```
GET    /api/workspace                       # repo info, mode, roots, projects, envs, tools
GET    /api/setup/detect                    # DetectionReport (setup mode)
POST   /api/setup/apply                     # write groundwork.yaml (+ optional files)
POST   /api/setup/scaffold/preview          # {form} → file tree + contents
POST   /api/setup/scaffold                  # write files / ?format=zip

GET    /api/files?path=&iac_only=1          # tree
GET    /api/files/content?path=             # {content, sha}
PUT    /api/files/content?path=             # If-Match sha → 200 | 409
GET    /api/git/status | /api/git/diff?path=

GET    /api/checks?unit=&severity=          # diagnostics
POST   /api/checks/run                      # {unit?|all}
POST   /api/checks/fix                      # apply QuickFix or fmt

GET    /api/runs?kind=&status=&cursor=
POST   /api/runs                            # {kind, target, options} → job
GET    /api/runs/{id}                       # detail + stages + summary
GET    /api/runs/{id}/log?from=&level=&q=   # paged NDJSON
POST   /api/runs/{id}/cancel
GET    /api/runs/{id}/plan                  # PlanSummary + resource diffs + danger flags
POST   /api/runs/{id}/approve               # {confirm_text, acknowledgements[]} → starts apply

GET    /api/graph/deps?root=&env=
POST   /api/graph/architecture              # {root|module, env, buffers{path:content}} (unsaved edits)
GET    /api/drift?env=      POST /api/drift/run   POST /api/drift/{id}/action

GET    /api/inventory?env=                  # groups, hosts, last play
GET    /api/inventory/hosts/{host}?env=     # facts + var provenance
POST   /api/inventory/ping | /facts | /adhoc

GET    /api/vars/terraform?root=            # matrix
GET    /api/vars/ansible?project=
PUT    /api/vars/terraform                  # {var, env, value, file}
GET    /api/secrets                         # list + decrypt status
POST   /api/secrets/{id}/reveal             # one-time value
POST   /api/secrets/scan  |  POST /api/secrets/move-to-sops

GET    /api/issues   POST /api/issues/{id}/resolve   POST /api/issues/{id}/actions/{n}
GET    /api/doctor   POST /api/doctor/run
GET    /api/onboarding   PATCH /api/onboarding

GET    /api/events                          # SSE: file.changed, checks.updated, run.*, log.line,
                                            #      drift.updated, issue.*, inventory.updated
```

Errors use one shape: `{error: {code, message, details}}`. Contract tests check every handler against an OpenAPI spec (`docs/openapi.yaml`). The frontend's TS client is generated from it with `openapi-typescript`.

---

## 10. Frontend plan

**Routes:**

| Route | Design file |
|---|---|
| `/` | `Main.dc.html` |
| `/code/*path` | `Code.dc.html`, `CodeVisual.dc.html` |
| `/runs`, `/runs/:id` | `Run.dc.html` |
| `/runs/:id/plan` | `Plan.dc.html` |
| `/graph` | `Graph.dc.html` |
| `/troubleshoot`, `/troubleshoot/:issue` | `Troubleshoot.dc.html` |
| `/inventory/:env?` | `Inventory.dc.html` |
| `/variables/:tab?` | `Variables.dc.html` |
| `/setup/detect` | `Detect.dc.html` |
| `/setup/scaffold` | `Scaffold.dc.html` |

**Shell.** One `AppShell` component: sidebar (repo, nav with live counts, environments, toolchain) plus a top bar. The ⌘K command palette searches files, resources, hosts and runs from a client-side index built from `/api/workspace` and the graph.

**Components to build first:** these primitives are shared across screens.
- `StatusPill`
- `Btn`
- `Seg`
- `Panel`
- `KV`
- `LogView` (virtualised, ANSI-free, level filter, follow)
- `DiffTable`
- `StagePipeline`
- `FileTree`
- `CodeEditor`
- `GraphCanvas`
- `ConfirmGate` (checkbox and typed text)
- `TourOverlay`

Build them from the design's CSS: copy the token values and spacing exactly.

**Live data.** One `useEvents()` composable opens the SSE stream and dispatches events to the Pinia stores. It reconnects with backoff and refetches snapshots after a reconnect.

**Log view.** Virtualised list (`vue-virtual-scroller`). Lines arrive in batches every 50 ms to avoid re-render storms. Search runs in the browser for loaded lines and on the server for the full log.

**Accessibility:**
- Real buttons, links and labels, as in the design.
- Focus rings use the accent colour.
- Every status has a text label.
- The tour is keyboard navigable (Esc closes, ←/→ step).

**Keyboard:** `⌘K` palette, `?` shortcuts sheet, `g o/c/r/g/t/i/v` go to a screen, `⌘S` save in the editor.

---

## 11. Data flow examples

**Edit → verify → visualise**
1. The user types in CodeMirror. The buffer goes to `POST /api/graph/architecture`, debounced. The diagram updates without saving.
2. ⌘S does a `PUT /files/content` with `If-Match`. The watcher fires `file.changed` → the checks service re-runs fmt/validate/tflint for that module → `checks.updated` SSE → gutter, Problems tray and sidebar counts update.

**Plan → approve → apply**
1. `POST /runs {kind: tf.plan}` → job: init (if needed) → validate → plan `-out` → `show -json` → PlanSummary + danger flags → status `waiting_approval`.
2. The Plan screen loads `/runs/{id}/plan`. Approval needs the acknowledgement checkbox (if danger) and the typed target name (if the env requires approval).
3. `POST /approve`: the server re-checks the plan hash, state serial, HEAD and TTL. If any is stale it returns 409 "Re-plan required". Otherwise it records the approval and starts the apply stage with the saved plan. Then it runs a refresh-only drift check as the final stage.

---

## 12. Security model

Groundwork runs commands that can change production, so the local server must be locked down:

- **Bind:** `127.0.0.1` (and `::1`) only. `--host` lets you bind other addresses only with an explicit `--i-understand-remote-access` flag and a warning banner.
- **Session token:** 32 random bytes generated at startup.
  - The first page load carries `?t=` and swaps it for an `HttpOnly; SameSite=Strict` cookie.
  - API calls also need an `X-Groundwork-Token` header that the SPA reads from a `<meta>` tag injected at serve time.
  - This blocks other local processes and web pages from calling the API.
- **DNS rebinding:** reject any request whose `Host` isn't `127.0.0.1:<port>`, `localhost:<port>` or `[::1]:<port>`.
- **CSRF:** reject state-changing requests whose `Origin` or `Referer` doesn't match. JSON-only bodies.
- **Filesystem:** all paths go through `SafePath`, so nothing outside the repo is reachable. The one exception is configured secret key files, which are read by the SOPS/vault adapters, never served.
- **Command construction:** argv slices only, never a shell. User-provided values (limit, tags, var values) are passed as discrete arguments and validated.
- **Mutations:** an approval record is required (§2). prod-like envs require typed confirmation. `force-unlock` always requires it.
- **Secrets:** redaction on all output. Reveal is explicit and audited. No secret ever goes in SQLite.
- **CSP:** `default-src 'self'`. No external fonts or scripts; everything is embedded.

---

## 13. Cross-platform & packaging

- **GoReleaser** targets: `darwin/{amd64,arm64}`, `linux/{amd64,arm64}`, `windows/amd64`. Every release ships checksums and an SBOM (syft), and is signed with cosign keyless.
- **Install channels:** a Homebrew tap (`brew install <you>/tap/groundwork`), `curl -fsSL …/install.sh | sh`, `go install …/cmd/groundwork@latest`, and `.deb`/`.rpm` via nfpm.
- **Build pipeline:** `make web` (vite build → `internal/server/dist`) runs before `go build`. CI checks that `dist` is fresh.
- **Version info** is injected with `-ldflags`. `groundwork version` prints the version, commit and the embedded UI build hash.
- **Self-update:** v1 only checks GitHub releases and shows a hint in the sidebar footer. It never downloads automatically.

---

## 14. Testing strategy

| Layer | What | How |
|---|---|---|
| Parsers | detection, HCL inspect, plan JSON, validate JSON, tflint/ansible-lint/yamllint output, callback events, rules matching | Table tests + **golden files** in `testdata/golden` (`-update` flag) |
| Adapters | terraform/ansible command building, log classification | Unit tests with a fake `exec` (record argv/env) |
| Integration (Terraform) | init/plan/apply/drift/unlock against fixture roots using `null`, `random`, `local` providers (no cloud) | `go test -tags=integration`, real terraform + tofu in CI matrix |
| Integration (Ansible) | inventory, playbook with callback, check/diff, unreachable host | Docker containers with sshd as targets in CI |
| API | every handler; auth/Host/Origin rejection; SafePath escapes | `httptest`, OpenAPI contract tests |
| Security | path traversal, DNS-rebinding headers, token absence, redaction | Dedicated test suite + fuzzing `SafePath` and `redact` |
| Frontend | components, stores | Vitest |
| E2E | each design screen's primary flow against `testdata/repos/iac-only` and `embedded` | Playwright; screenshot comparisons vs design at 1440 width |

---

## 15. Milestones

Estimates are rough **focused-engineer weeks** for one person. Each milestone ends in a usable build.

### M0 — Skeleton (1 wk)
- Cobra CLI, `serve`, the embedded SPA hello page, token auth plus Host/Origin middleware, SSE hub, slog, SQLite plus migrations, `.groundwork/` bootstrap, browser open, single-instance detection.
- CI: lint (golangci-lint), test, build matrix, `make web`.
- **Done when:** `groundwork` opens an authenticated blank shell. Requests without the token are rejected (tested).

### M1 — Detect, config, scaffold (2 wks) · *Detect, Scaffold*
- Detection engine with evidence and fixture goldens, `groundwork.yaml` schema/load/save, setup mode routing.
- Detect screen: findings, env mapping, YAML preview, write.
- Scaffold presets (start with `aws-envs` and `onprem`), preview, zip, safe write.
- **Done when:** it works on 3 real-world-shaped fixtures (IaC-only, app repo with `deploy/terraform` and `ops/ansible`, empty) and produces correct config with no manual edits.

### M2 — Code explorer + checks (2.5 wks) · *Code*
- File tree (IaC-only / All), CodeMirror with HCL/YAML, save with conflict detection, git status/diff.
- Checks pipeline: fmt, validate (temp data dir), tflint, ansible-lint, yamllint, syntax-check; the Diagnostic model; incremental on-save runs; Problems tray; gutter markers; did-you-mean quick fix.
- **Done when:** saving a file with an error shows the diagnostic in under 2 s for a typical module, and the quick fix applies.

### M3 — Runner + Terraform runs + basic Overview (2.5 wks) · *Run, Main (basic)*
- Job queue and locks, process groups, cancel, NDJSON logs, redaction v1.
- Terraform init/validate/plan/apply with stages and per-resource apply progress.
- Run list and detail, live log view (search, level filter, follow).
- Overview v1: env cards (last run and pending changes from the last plan), recent runs, verification summary.
- **Done when:** a fixture apply streams live, cancel releases the lock cleanly, and a restart marks interrupted runs.

### M4 — Plan review & approval (1.5 wks) · *Plan*
- `show -json` → PlanSummary, attribute diffs, replace detection, danger rules, staleness checks, approval gate with typed confirmation, approve → apply of the saved plan.
- **Done when:** approval is impossible after the state or HEAD changes (409, tested), and the danger banner fires for a DB replacement fixture.

### M5 — Ansible + Inventory (2.5 wks) · *Inventory, Run (ansible)*
- Callback plugin and event parsing, playbook runs with a per-host/task summary, check/diff mode, limit/tags.
- Inventory screen: groups, hosts, ping, facts cache, var provenance, ad-hoc commands with confirmation.
- **Done when:** a playbook run against Docker targets shows ok/changed/unreachable per host, and Inventory's "last play" updates live.

### M6 — Graph + Visualize (2.5 wks) · *Graph, CodeVisual*
- Dependency graph from DOT/static analysis with plan and drift overlays. elkjs layout, inspector, export.
- Architecture view from the unsaved buffer, with containment rules for AWS first, then GCP/Hetzner, cursor sync and error overlay.
- Drift: refresh-only jobs, the code-vs-actual table, and the three drift actions.
- **Done when:** editing `subnet_ids` in the editor updates the diagram without saving, and drift on a fixture shows in Graph and on Overview.

### M7 — Variables & secrets (2 wks) · *Variables*
- TF var matrix with sources, missing/unused/differs, editing via hclwrite.
- Ansible var matrix.
- SOPS decrypt (Go lib), vault decrypt (CLI), reveal flow, plaintext scan, move-to-SOPS.
- **Done when:** the matrix matches `terraform console` values for fixtures, revealed secrets never appear in the DB, logs or server output (tested), and the scan catches the test patterns.

### M8 — Troubleshoot, Doctor, drift schedule, onboarding (2 wks) · *Troubleshoot, Main (final)*
- Rules engine and the initial 20 rules, the issue lifecycle, Troubleshoot list/detail with automated pre-checks and actions.
- Doctor checks, scheduled drift with desktop/webhook notifications.
- Overview "Needs attention", first-run tour and data-driven checklist, ⌘K palette, keyboard shortcuts.
- **Done when:** each rule has a fixture log that triggers it, and the tour and checklist behave as designed on a fresh repo.

### M9 — Hardening & v0.1.0 release (1.5 wks)
- Security test suite, fuzzing, performance on large repos (5k .tf files), Windows smoke test, docs site (`docs/`), GoReleaser, Homebrew tap, install script, a demo fixture repo and screenshots.
- **Done when:** a clean install on macOS and Linux works end-to-end on the demo repo in under 5 minutes.

**Total ≈ 20 focused weeks.** Two milestones can run in parallel once M3 lands: M5 (Ansible) is independent of M4 and M6.

---

## 16. Risks & open questions

| Risk / question | Mitigation / decision needed |
|---|---|
| Terraform's BSL licence | Groundwork shells out to the user's binary, which is fine. Supporting OpenTofu equally removes the dependency. |
| `validate` needs `init` (providers download) | Use a separate `TF_DATA_DIR` under `.groundwork/tfdata/<root>` with `init -backend=false`. That avoids touching real state; the cost is a provider cache (`TF_PLUGIN_CACHE_DIR`) to keep it fast. |
| Architecture view accuracy varies by provider | Ship AWS rules first. Show "unmapped resources" in a side tray rather than guessing. Rules live in a data file so the community can add more. |
| Ansible variable precedence is complex | Show provenance only when groundwork's resolver agrees with `ansible-inventory --host`; otherwise say "can't determine source". Document the supported subset. |
| Long-running applies when the browser closes | Jobs run in the server, not the browser. A closed tab doesn't stop them. Quitting the server prompts if jobs are running (CLI asks; SIGINT does a graceful stop of the queue). |
| Windows + Ansible | Doctor explains WSL. Terraform features work natively. |
| Credentials expiry mid-apply | Doctor shows the expiry. A pre-apply check warns if the session expires in under 10 minutes. |
| Large logs / huge plans | Paged log API, virtualised UI, plan diff lazy-loads per resource. |
| Name | "groundwork" vs "iacg": decide before M9 (binary name, config file name, Homebrew formula). |

---

## 17. After v1 (backlog)

- Infracost integration (free CLI) for cost deltas on plans.
- Run history charts (duration, success rate per stack).
- A per-resource change timeline (runs and commits touching an address).
- Blast-radius highlighting before apply.
- Policy-as-code (OPA/Conftest) as a check, with per-env "blocking" settings.
- Import helper (generate `import {}` blocks plus starter HCL).
- Provider, module and collection upgrade assistant with changelogs.
- Module and role catalogue with "add to stack" forms.
- Plugin interface (Go plugins via a gRPC subprocess protocol) for OpenTofu-specific, Pulumi, Helm and Packer adapters.
- A multi-repo launcher (`groundwork ~/code/*`).
- An optional committed run history (`.groundwork/history/` export) for teams that share a repo.

---

## 18. Definition of done (every feature)

- Matches the design in `design/` (layout, copy, states, including empty, loading and error states that the mockups imply).
- Keyboard accessible, with visible focus and text labels on statuses.
- Covered by unit tests and at least one integration or e2e test.
- No secret leakage (redaction test passes).
- Works offline, and degrades gracefully when an optional tool is missing.
- Documented in `docs/` with a screenshot.
