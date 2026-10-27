# Runs

A **run** is one job against one target: a Terraform root and environment, or an Ansible project and environment ([Ansible jobs](ansible.md#jobs)). Start a Terraform plan from the Overview (the **Plan** button), or a [destroy](#destroy) (**⋯ → Destroy…**). Nothing changes real infrastructure without an approval.

```
init → [workspace] → validate → plan → summary → approve → apply
```

`workspace` appears for roots that use workspaces per environment. `summary` (reading the plan back with `show -json`) is plumbing and is only shown if it fails.

## The run page

Under the stage pipeline are two tabs. **Changes** opens first (except for `init`, which opens on its log):

- **Terraform:** one tile per planned change, grouped by module. The tile's left edge is the action (create, update, replace, destroy). Tiles are faded until they start, pulse while running, and turn solid when done or red when failed, with a progress bar above.
- **Ansible:** a grid with a row per task and a column per host, each cell coloured ok, changed, failed or skipped. Selecting a cell shows the task's output and diff; the approval gate for a dry run sits above the grid.

Selecting a tile or a host name opens **Log** searched for it. The Log tab shows the warning and error counts. The tab is kept in the URL (`?tab=log`).

### The header

- While a run is going, the line under the title counts its time and, once at least two earlier runs of the same kind on the same root (or playbook) and environment have finished, says how long it usually takes ("usually 2–3 min"). The estimate is for the part that is running: up to the approval for a plan, after it for an apply; time spent waiting for approval is left out. A finished run shows when it ended instead: "failed 14:30:21 after 2m 41s".
- **When it finishes ▾** (shown while the run is going) chooses whether to hear back about this run only: **When it finishes**, **Only if it fails**, **Don't notify**, or **Project default** (the `notify:` settings, summarised in the menu). It overrides the project setting for this one run.
- **Download log** and **Cancel run** stay on the header. While a Terraform plan waits for approval, a banner under the stages says what approving does ("This plan creates 3 resources and destroys nothing"), with **Review plan and approve** and **Discard** side by side; it turns red when the plan replaces or destroys anything. Cancelling and discarding ask on the page first ("Cancel this run? Terraform will be interrupted and asked to release its state lock.", or "Discard this plan?"). **⋯** holds **Copy command**, the full plan diff and, for a failure with a known cause, its Troubleshoot entry.
- In the Runs list, a dot marks runs that ended while you weren't looking. Opening the run clears it, and its entry under the bell.

### When a run fails

The red error banner becomes a card that explains the failure:

- If [Troubleshoot](troubleshooting.md) matched the error to a known cause, the card leads with it (marked **known cause**) and lists the facts that decide the next step. For a state-lock error these come from Terraform's lock info: who holds the lock, since when ("since 14:21 (9 min)") and for what operation, so a stale lock can be told from a live CI job. It also says where the run stopped (the failed resource, or the failed hosts for Ansible) and what already changed: "applied 1 of 3 · nothing was rolled back" for an apply, nothing for a plan that never reached it.
- **Plan again** (the primary action; **Dry run again** for a playbook) starts a new plan for the same target. The cause's first fix, such as **Release the lock…**, opens a confirmation on the page with Troubleshoot's pre-checks, the command it runs and, where needed, the environment name to type.
- **Show in log** opens the Log tab filtered to errors. **Raw error** expands the original text.
- Without a known cause, the card shows the error's first line, cleaned of exit codes and repeated "Error:" prefixes, with the same **Show in log** and **Raw error**.

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

## Destroy

**Destroy…**, in the **⋯** menu on an environment's card on the Overview, tears down everything Terraform manages there. It is a plan like any other, never a direct `terraform destroy`:

1. **Destroy…** asks on the page first ("Plan the destruction of everything in dev?"). Confirming starts a run with `terraform plan -destroy`. Nothing is destroyed yet.
2. The review page says it's a **destroy plan** and lists every resource to be removed, with the usual danger rules: data-bearing resources need the acknowledgement box.
3. Approving always needs the environment's name typed, whatever the `approval` setting, dev included.
4. The approved plan is applied as saved (`terraform apply <plan>`), with the same freshness checks. The run, its notifications and history read **terraform destroy**.

**Re-plan** of a destroy plan plans a destroy again.

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
  run_timeout: 2h        # a run past this is stopped and fails (default 2h)
ansible:
  run_timeout: 2h
retention:
  runs: 500              # keep this many runs per project (default 500)…
  days: 30               # …and none older than this (default 30)
```

Retention prunes finished runs (and their logs and plans) when the project is opened; runs in progress or waiting for approval are always kept.

### Secrets in plans

`terraform show -json` contains raw values, including sensitive ones. groundwork reads it once, computes masked diffs into `diff.json` (mode 0600) and discards the raw JSON. Values Terraform marks sensitive are masked, **and so is every other occurrence of those values**: Terraform doesn't always propagate the marking (`terraform_data` copies a sensitive input into an unmarked output, for example). Sensitive variables' values and well-known token formats are masked too.

## Locking and cancelling

- One active job per Terraform root. Others queue in arrival order. Different roots run in parallel. The lock is per root, not per workspace: `terraform workspace select` rewrites `.terraform/` for the whole directory, so two workspaces of one root can't safely run together.
- **Cancel** sends SIGINT to the job's process group so Terraform stops gracefully and releases its state lock. If it hasn't exited after 15 s it is killed, and an issue ("possible stale lock") is raised for that root.
- Stopping the service cancels running jobs the same way and waits up to 25 s ([stopping](service.md#starting-it)).
- When a project is opened, runs that were in flight when the service died are marked **failed (interrupted)**. If one was applying, a stale-lock issue is raised; if its tool is still running, an issue names the process group. Old runs are then pruned (see `retention` above).
- A run that passes its `run_timeout` (default 2 h) is stopped the same way and fails.

## Logs

- Plan and apply use Terraform's `-json` stream, so per-resource progress (creating → done / failed, "still creating… 30s") is structured, not scraped from text. Diagnostics are formatted the way the CLI prints them.
- Every line is **redacted** before it is stored or sent: AWS keys, bearer and provider tokens, `password=`/`secret=`/`token=` values, PEM private keys, and the values of secret-looking environment variables (`*_TOKEN`, `*_SECRET*`, `*PASSWORD*`, ...). `terraform show -json`, which can contain secrets, is never logged; it is kept in `plan.json` under `.groundwork/` and only a summary (addresses and actions, no values) is stored.
- Logs are paged and filterable by level and text. A run keeps the first ~45 MB and the last 2,000 lines of its log; live viewers see everything.
- The run's exact command is copied by **Copy command** (under **⋯** on the run page). Commands are argv lists, never shell strings.

## API

The HTTP API is private to the bundled UI and may change in any release ([compatibility](compatibility.md)); scripts should use the CLI. For contributors, the run endpoints, each under `/api/p/{project}`:

```
POST /runs {kind: "tf.plan"|"tf.init"|…, root, env, upgrade?}   → 202
GET  /runs?status=&kind=&cursor=      GET /runs/{id}
GET  /runs/{id}/log?from=&limit=&level=&q=    …/log/download
GET  /runs/{id}/resources             per-resource progress
GET  /runs/{id}/hosts                 per-host results (Ansible)
GET  /runs/{id}/plan                  summary, danger, freshness, state serial, checks
GET  /runs/{id}/plan/resource?address=   one resource's masked attribute diff + source location
POST /runs/{id}/approve {confirm_text, acknowledge_danger}   409 stale_plan · 422 ack_required/confirm_mismatch
POST /runs/{id}/replan                POST /runs/{id}/cancel
GET  /runs/{id}/notify                PUT /runs/{id}/notify {mode: "default"|"finish"|"fail"|"none"}
GET  /issues?run={id}                 the Troubleshoot issues matched to a run
GET  /envs                            per-environment status for the Overview
```

`root` and `env` are matched against `groundwork.yaml`; anything else is refused. SSE events: `run.updated`, `log.line`.

## Testing

`make test` uses a scriptable fake `terraform` (`testdata/fake-terraform`) replaying real captured output (`testdata/terraform`). `make integration` also runs the real binary: a full plan → approve → apply, a re-plan that finds no changes, a failed apply, a cancel mid-apply that must release the state lock, and an out-of-band `terraform apply` after planning that must make approval fail.
