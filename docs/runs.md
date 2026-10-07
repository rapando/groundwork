# Runs

A **run** is one job against one Terraform root and environment. Start one from the Overview (the **Plan** button) or `POST /api/runs`. Nothing changes real infrastructure without an approval.

```
init → [workspace] → validate → plan → summary → approve → apply
```

`workspace` appears for roots that use workspaces per environment. `summary` (reading the plan back with `show -json`) is plumbing and is only shown if it fails.

## Plan, review, approval, apply

- A plan is written to `.groundwork/runs/<id>/tfplan`. If it has changes the run stops at **needs approval** and releases its lock; if not, the run succeeds and `approve`/`apply` are skipped.
- **Review** (`/runs/<id>/plan`) shows add/change/replace/destroy tiles, a resource list filterable by action, and per-resource attribute diffs (before → after) loaded on demand. Attributes that force a replacement are marked and listed first; values only known after apply are shown as such; each resource links to its declaration in the editor.
- **Approve** applies *that saved plan file*. Nothing is re-planned, so what you reviewed is exactly what runs. It is refused (409, "re-plan required") when:
  - the plan file no longer matches its recorded SHA-256;
  - the plan is older than `terraform.plan_ttl` (default `1h`);
  - the configuration changed: files in the root, the local modules it uses (from `.terraform/modules/modules.json`), or its var files. This also catches uncommitted edits, which a git-HEAD comparison would miss;
  - the state changed: `terraform state pull` shows a different serial or lineage, or state appeared/disappeared, since the plan.
- The same freshness checks run **again under the root lock immediately before `terraform apply`**, so a state change while the apply waits in the queue is caught too.
- Environments that require approval (`prod`-like names by default, or `approval: required`) need the environment name typed.
- A consumed plan file is deleted after the apply, successful or not: a partial apply leaves it stale, so re-plan to continue. **Re-plan** discards a waiting plan and starts a new one for the same target.
- A plan waiting for approval survives a restart.

### Danger rules

Destroying or replacing a resource whose type matches one of these needs an explicit acknowledgement on top of any typed confirmation:

| Category | Patterns |
|---|---|
| database | `*_db_*`, `*_rds_*`, `*_database*` |
| bucket | `*_bucket*` |
| volume / disk | `*_volume*`, `*_disk*` |
| protected | any resource with `lifecycle { prevent_destroy = true }` in another environment (same `type.name`) |
| custom | patterns listed under `terraform.danger` in `groundwork.yaml` |

```yaml
terraform:
  plan_ttl: 30m
  danger: ["*_cache_cluster", "*_kms_key"]
```

### Secrets in plans

`terraform show -json` contains raw values, including sensitive ones. groundwork reads it once, computes masked diffs into `diff.json` (mode 0600) and discards the raw JSON. Values Terraform marks sensitive are masked, **and so is every other occurrence of those values**: Terraform doesn't always propagate the marking (`terraform_data` copies a sensitive input into an unmarked output, for example). Sensitive variables' values and well-known token formats are masked too.

## Locking and cancelling

- One active job per Terraform root. Others queue in arrival order. Different roots run in parallel. (The original plan locked per root *and workspace*, but `terraform workspace select` rewrites `.terraform/` for the whole directory, so two workspaces of one root can't safely run together.)
- **Cancel** sends SIGINT to the job's process group so Terraform stops gracefully and releases its state lock. If it hasn't exited after 15 s it is killed, and an issue ("possible stale lock") is raised for that root.
- Stopping groundwork itself (Ctrl-C) cancels running jobs the same way and waits up to 25 s.
- On startup, runs that were in flight when the process died are marked **failed (interrupted)**. If one was applying, a stale-lock issue is raised. Runs older than 30 days, or beyond the newest 500, are pruned.

## Logs

- Plan and apply use Terraform's `-json` stream, so per-resource progress (creating → done / failed, "still creating… 30s") is structured, not scraped from text. Diagnostics are formatted the way the CLI prints them.
- Every line is **redacted** before it is stored or sent: AWS keys, bearer and provider tokens, `password=`/`secret=`/`token=` values, PEM private keys, and the values of secret-looking environment variables (`*_TOKEN`, `*_SECRET*`, `*PASSWORD*`, ...). `terraform show -json`, which can contain secrets, is never logged; it is kept in `plan.json` under `.groundwork/` and only a summary (addresses and actions, no values) is stored.
- Logs are paged and filterable by level and text. A run keeps the first ~45 MB and the last 2,000 lines of its log; live viewers see everything.
- The run's exact command is shown by **Copy command**. Commands are argv lists, never shell strings.

## API

```
POST /api/runs {kind: "tf.plan"|"tf.init", root, env, upgrade?}   → 202
GET  /api/runs?status=&kind=&cursor=      GET /api/runs/{id}
GET  /api/runs/{id}/log?from=&limit=&level=&q=    …/log/download
GET  /api/runs/{id}/resources             per-resource progress
GET  /api/runs/{id}/plan                  summary, danger, freshness, state serial, checks
GET  /api/runs/{id}/plan/resource?address=   one resource's masked attribute diff + source location
POST /api/runs/{id}/approve {confirm_text, acknowledge_danger}   409 stale_plan · 422 ack_required/confirm_mismatch
POST /api/runs/{id}/replan                POST /api/runs/{id}/cancel
GET  /api/envs                            per-environment status for the Overview
```

`root` and `env` are matched against `groundwork.yaml`; anything else is refused. SSE events: `run.updated`, `log.line`.

## Testing

`make test` uses a scriptable fake `terraform` (`testdata/fake-terraform`) replaying real captured output (`testdata/terraform`). `make integration` also runs the real binary: a full plan → approve → apply, a re-plan that finds no changes, a failed apply, a cancel mid-apply that must release the state lock, and an out-of-band `terraform apply` after planning that must make approval fail.
