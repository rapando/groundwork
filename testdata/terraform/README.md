# Captured terraform output

Real output from terraform 1.16.5 (built-in `terraform_data` only), used as parser fixtures:

| File | Command |
|---|---|
| `plan.jsonl`, `apply.jsonl` | `plan -json` / `apply -json` of examples/terraform-only (3 creates) |
| `apply_failed.jsonl` | apply where a `local-exec` provisioner exits 3 |
| `plan_locked.jsonl` | `plan -json` while another apply holds the (local) state lock |
| `plan_error.jsonl` | plan with two required variables unset |
| `plan_mixed.jsonl`, `show_mixed.json` | update + replace (`triggers_replace`) + delete + a no-op |
| `show_plan.json` | `show -json` of the 3-create plan |
| `show_sensitive.json` | replace of a resource with a sensitive input (`hunter2-SECRET` → `n3w-SECRET-pw`), nested map/list, unknowns |
| `state_pull.json` | `state pull` after an apply (serial 3) |
| `init.txt` | `init` output |
| `graph_plan.dot` | `graph -type=plan` of examples/terraform-only/envs/prod |
| `drift_plan.jsonl`, `show_drift.json` | refresh-only plan after a `local_file`'s file was changed outside Terraform (the provider reports it as deleted) |

`show_danger.json` is **derived**: `show_mixed.json` with two resources renamed to
`aws_db_instance.main` (replace) and `aws_s3_bucket.logs[1]` (delete), so danger
rules can be tested without a cloud provider. `show_drift_update.json` is likewise
`show_drift.json` with the drift turned into an in-place update (`content`, `file_permission`
changed), because the `local` provider never reports attribute drift. Everything else is verbatim.
