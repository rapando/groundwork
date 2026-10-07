# Troubleshooting, Doctor and scheduled drift

## Issues

When a run fails (or succeeds with a warning worth knowing about), groundwork matches its log against a set of **rules**. A match becomes an **issue**: what went wrong in plain words, the facts pulled out of the error (lock ID, variable name, host), and the steps to fix it. Check findings with a known explanation (yamllint indentation, ansible-lint `no-changed-when`) become issues the same way.

Open issues appear on the Overview under **Needs attention** and on the **Troubleshoot** screen, with the raw error kept underneath.

**Lifecycle.** An issue is identified by rule, target (a Terraform root and environment, an Ansible project and environment, or a file) and, where it applies, a host or variable, so the same problem recurring updates one issue instead of piling up. It resolves on its own when the evidence says it's fixed:

- a later run of a kind that would have hit the problem succeeds on the same target (for example a plan or `force-unlock` after a stale lock; `init` after a lock-file problem; for per-host Ansible issues, that host finishing cleanly);
- for findings, the check passes again.

You can also mark an issue resolved. Old evidence never re-opens a resolved issue; a new failure does. "Resolved this week" counts both.

**Fix steps** are one of: *run* (starts a groundwork job: init, plan, force-unlock, ping), *open* (a screen or file), or *copy* (a command to run yourself, such as `aws sso login`). Run steps are built on the server from the rule and the stored issue, never from the browser. Steps that change state need the environment name typed, and are blocked while their automated pre-checks fail (for a stale lock: no groundwork job on that root, no `terraform` process on this machine).

**Export report** downloads a Markdown summary of open and recently resolved issues plus the last Doctor run, for a ticket or chat.

### Built-in rules

| Rule | Fires on |
|---|---|
| `tf-state-lock-stale` | `Error acquiring the state lock` → force-unlock (typed confirmation, pre-checks), re-plan |
| `tf-provider-checksum` | provider package doesn't match `.terraform.lock.hcl` → `init -upgrade` |
| `tf-lock-file-inconsistent` | `Inconsistent dependency lock file` → `init` |
| `tf-unsupported-argument` | `Unsupported argument` (log or validate finding), with Terraform's "did you mean" |
| `tf-missing-variable` | `No value for required variable`, one issue per variable → Variables screen |
| `tf-provider-version-conflict` | `no available releases match the given constraints` |
| `tf-module-not-installed` | `Module not installed`, `Module source has changed` → `init` |
| `tf-module-source-not-found` | `Unreadable module directory`, `Failed to download module` |
| `tf-backend-init-required` | `Backend initialization required` (you choose `-reconfigure` or `-migrate-state`) |
| `tf-backend-access-denied` | S3 403 / `AccessDenied` loading or saving state, KMS access denied |
| `aws-sso-expired` | expired AWS SSO session → `aws sso login` |
| `aws-no-credentials` | `No valid credential sources found` |
| `ans-unreachable-timeout` / `-refused` | SSH timeout / connection refused, per host |
| `ans-host-key-changed` | `REMOTE HOST IDENTIFICATION HAS CHANGED`, per host |
| `ans-ssh-auth-failed` | `Permission denied (publickey…)`, per host |
| `ans-vault-decrypt-failed` | vault password doesn't open a value |
| `ans-vault-no-password` | vault data but no password configured |
| `ans-become-password` | `Missing sudo password` / `sudo: a password is required` |
| `ans-python-interpreter` | discovered-interpreter warning → pin `ansible_python_interpreter` |
| `yamllint-indentation`, `ansible-lint-no-changed-when`, `ansible-lint-name-casing` | explanations for those findings |

Each rule has a fixture in `internal/diagnostics/testdata` that triggers it; most are captured from real terraform 1.16 and ansible-core 2.21 output.

### Writing your own rules

Put YAML files in `.groundwork/rules/`. A rule with a built-in's `id` replaces it; new IDs add rules. Broken files are reported on the Troubleshoot screen and skipped.

```yaml
- id: corp-proxy
  scope: any                # terraform | ansible | any
  category: network         # the label on the pill
  severity: error           # error | warning | info
  match:
    any:
      - log: 'proxyconnect tcp: .*refused'          # Go regular expression over the run log
      # - diagnostic: { tool: tflint, code: '^aws_instance_invalid_type$' }
  extract:
    proxy: 'proxyconnect tcp: dial tcp ([^:]+:\d+)'
  title: 'Proxy {{ .proxy }} refused the connection'
  explain: >
    Downloads go through the corporate proxy, which refused the connection. Connect to the VPN and re-run.
  steps:
    - title: Re-plan once you're on the VPN
      action: { kind: run, label: 'Plan {{ .Target }}', run: { kind: tf.plan } }
  resolved_by: [tf.init, tf.plan]
```

`each:` (a regex with named groups, such as `(?P<host>…)`) makes one issue per match. Templates see `.Target`, `.Root`, `.Env`, `.Project`, `.Host`, `.Run.ID`, `.Run.Kind`, the `extract` names, and a `path` function that joins non-empty parts. Rules are re-read every 30 seconds.

## Doctor

**Run doctor** on the Troubleshoot screen, or `groundwork doctor` in a terminal (exit code 1 if anything fails; `--json` for scripts). Each check reports ok, warn or fail with a hint:

- **Tools**: terraform/tofu, ansible and the enabled linters, with versions.
- **Cloud credentials**, for providers the code uses: AWS (`aws sts get-caller-identity`, plus the SSO session's expiry from `~/.aws/sso/cache`), Google (`gcloud auth application-default print-access-token`), Azure (`az account show`).
- **State backend**: for S3, `aws s3api head-bucket`; **state locking**: S3 `use_lockfile` or the DynamoDB table (`describe-table`). Other backends are listed as not checked.
- **SSH agent**: `ssh-add -l`.
- **Inventory reachability**, from the last ping of each environment.
- **Vault password file**: present and not readable by other users, for projects that use ansible-vault.
- **Disk space** where `.groundwork/` lives, and whether the repo is a git repository.

Doctor only reads: nothing it runs changes anything.

## Scheduled drift

```yaml
drift:
  schedule: "0 */6 * * *"   # cron, 5 fields
  notify: desktop           # desktop | webhook | none
  webhook: https://hooks.example.com/…
```

While groundwork is running it checks every Terraform environment on that schedule with a refresh-only plan (it changes nothing; each check queues behind other jobs on the same root). When the set of drifted resources changes, it notifies once: a desktop notification (macOS Notification Center, or `notify-send` on Linux), or a JSON POST to the webhook with a Slack-compatible `text` field plus `root`, `env`, `drift_count`, `addresses` and `run_id`. Unchanged drift isn't re-sent; drift that goes away and comes back is. The Overview shows the schedule and the next run. Edits to `groundwork.yaml` apply without a restart.

`groundwork drift [--env prod]` runs the same check once from a terminal: exit code 1 if anything drifted, 3 if a check failed.

## First run and shortcuts

On a new repo the Overview shows a short tour and a **Getting started** checklist. The checklist ticks itself from what you've actually done (scanned, checks ran, a plan or dry run, the infrastructure view opened, Doctor ran). Replay the tour from the **?** button; hide the checklist for good with **Hide**. To never see tips, set `{"tips": false}` in `~/.config/groundwork/settings.json` (on macOS `~/Library/Application Support/groundwork/settings.json` works too), or start groundwork with `GROUNDWORK_TIPS=off`.

| Keys | |
|---|---|
| ⌘K / Ctrl-K, or `/` | jump to a file, run, host, environment, issue or action (plan an environment, run checks, run doctor) |
| `g` then `o` `c` `r` `g` `i` `v` `t` | Overview, Code, Runs, Graph, Inventory, Variables, Troubleshoot |
| `?` | all shortcuts |

Shortcuts are ignored while you type in the editor or a field.
