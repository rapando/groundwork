# Checks

`groundwork check` and the Code screen run the same pipeline. Nothing here plans, applies, or touches real state.

## Units and tools

A **unit** is a Terraform root, a Terraform module directory, or an Ansible project from `groundwork.yaml`. Editing a file re-checks its unit; editing a module also re-checks every root that uses it (transitively).

| Unit | Tools (if enabled in `checks.enabled`) |
|---|---|
| Terraform root | `fmt`, `validate`, `tflint`, `checkov` |
| Terraform module | `fmt`, `tflint`, `checkov` (`validate` runs through its roots) |
| Ansible project | `yamllint`, `ansible-lint`, `syntax-check` |

A tool that isn't installed is reported as **skipped** with an install hint, never as a failure. A tool that is installed but can't run (for example `terraform init` can't reach the registry) is **failed**, and the previous good diagnostics are kept.

## Running

- **Save in the UI** starts the unit's checks immediately.
- **External edits** are picked up by the file watcher (300 ms debounce, then 400 ms per unit) when `checks.on_save` is true.
- Results are cached by a hash of the unit's files (plus the modules a root uses), so an unchanged unit costs nothing.
- Nothing runs at startup. The first `validate` for a root can need `terraform init -backend=false` (provider downloads); groundwork never does that until you ask or save.

```
groundwork check [--json] [--unit tf:terraform/envs/dev] [--fail-on error|warning]
```

Exit codes: `0` clean, `1` findings at/above `--fail-on`, `2` usage or configuration problem, `3` a tool failed to run, so the result is incomplete.

## Why `validate` runs in a shadow tree

`terraform init` writes `.terraform.lock.hcl` next to the configuration, and `TF_DATA_DIR` only moves `.terraform/`. To keep the repo untouched, `init` and `validate` run in `.groundwork/shadow/<id>/`: every directory on the path to the unit is real, every sibling is a symlink to the real content. Relative module sources and `file("../x")` still resolve; the unit's own directory is real, so the lock file lands there. Your lock file, if any, is copied in so provider versions follow it. `init` calls are serialised because Terraform's plugin cache is not safe for concurrent writers; a `TF_PLUGIN_CACHE_DIR` you have set is respected.

If the shadow tree can't be built, `validate` reports **failed** rather than running against your repo.

## Differences from the original plan

- `fmt` runs per directory, not `-recursive`, since every module is its own unit; recursive would report files twice.
- "Did you mean" fixes come from Terraform's own suggestion in the diagnostic (Terraform 1.x includes it), verified against the exact range before offering the edit. A provider-schema Levenshtein fallback is not implemented yet.
- `tflint` runs with its working directory set to the unit, not `--chdir`: with `--chdir` it prints paths relative to its original cwd with inconsistent symlink handling.
- `ansible-playbook --syntax-check` output is parsed in both the ansible-core ≥ 2.19 format (`[ERROR]: … Origin: file:line:col`) and the older `ERROR! … The error appears to be in` format.
- `ansible-lint -f json` reports positions as `positions.begin.line` for some rules and `lines.begin` for others; both are handled.

## Editing safety

Saves are atomic (temp file + rename) and require `If-Match` with the sha you loaded; a mismatch is a 409 and nothing is written. Quick-fix edits are applied to the editor buffer (never straight to disk) and only if the text at the fix's range still equals what the fix expects. `.git/`, `.groundwork/`, `.terraform/` and `*.tfstate*` are never editable through the API.

## Testing against real tools

```
make integration                       # real terraform / tflint / yamllint / ansible-lint if installed
GW_TEST_NETWORK=1 make integration     # also downloads providers (aws, null) to validate the AWS scaffold
```

The parsers are tested against captured output of the real tools in `testdata/checks/`.
