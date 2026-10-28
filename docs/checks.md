# Checks

`groundwork check` and the Code screen run the same pipeline. Nothing here plans, applies, or touches real state.

## Units and tools

A **unit** is a Terraform root, a Terraform module directory, or an Ansible project from `groundwork.yaml`. Editing a file re-checks its unit; editing a module also re-checks every root that uses it (transitively).

| Unit | Tools (if enabled in `checks.enabled`) |
|---|---|
| Terraform root | `fmt`, `validate`, `tflint`, `checkov`, `secrets` |
| Terraform module | `fmt`, `tflint`, `checkov`, `secrets` (`validate` runs through its roots) |
| Ansible project | `yamllint`, `ansible-lint`, `syntax-check` (`ansible-playbook --syntax-check`), `secrets` |

`secrets` is built in: it scans the unit's files for plaintext credentials (a Terraform unit's own directory, an Ansible project recursively) and needs no tool. See [the plaintext secret scan](variables.md#plaintext-secret-scan).

A tool that isn't installed is reported as **skipped** with an install hint, never as a failure. A tool that is installed but can't run (for example `terraform init` can't reach the registry) is **failed**, and the previous good diagnostics are kept.

## Running

- **Save in the UI** starts the unit's checks immediately. With **Autosave** ticked on the Code screen (the default, remembered per browser), the editor saves a second after your last edit, so checks follow your typing; leaving the file saves anything still pending.
- **Format on save** (Terraform files, ticked by default and remembered per browser) runs `terraform fmt` (or `tofu fmt`) on the buffer before it is written, so the formatted text is what's saved and checked; one undo reverts it. It applies when you save with ⌘S or Save, and when leaving the file saves for you, but not to autosave's timer, which would move text under your cursor. A file that doesn't parse is saved as it is, and the notice says why.
- **External edits** are picked up by the file watcher (300 ms debounce, then 400 ms per unit) when `checks.on_save` is true.
- Results are cached by a hash of the unit's files (plus the modules a root uses), so an unchanged unit costs nothing.
- Nothing runs at startup. The first `validate` for a root can need `terraform init -backend=false` (provider downloads); groundwork never does that until you ask or save.

```
groundwork check [--json] [--unit tf:terraform/envs/dev] [--fail-on error|warning]
```

Exit codes: `0` clean, `1` findings at/above `--fail-on`, `2` usage or configuration problem, `3` a tool failed to run, so the result is incomplete.

## Providers

When you save a change to a root's `required_providers` (in the root or a local module it uses), groundwork installs the providers in the background, so the next plan doesn't wait for the download:

- It acts once **every** entry has both `source` and `version`, and the files parse; a half-written block is left alone. It waits until the root's files have been unchanged for 3 seconds, so autosave doesn't start a download per keystroke.
- It runs `terraform init -backend=false` (or `tofu`) in the root, which downloads what's missing and updates `.terraform.lock.hcl` (commit it, as usual). The backend and state are never touched.
- Then it deletes from the root's `.terraform/providers` every provider the lock file no longer lists, and older versions of the ones it does. A link into your own `TF_PLUGIN_CACHE_DIR` is removed as a link; the cache itself is never touched.
- Nothing runs while the root's requirements already match its lock file, or while a plan or apply holds the root (that run's own `init` installs them). A failed install isn't retried until the requirements change. The Code screen says when an install starts, finishes or fails.

Turn it off with `install_providers: false` under `terraform` in `groundwork.yaml`.

## Why `validate` runs in a shadow tree

`terraform init` writes `.terraform.lock.hcl` next to the configuration, and `TF_DATA_DIR` only moves `.terraform/`. To keep the repo untouched, the checks' `init` and `validate` run in `.groundwork/shadow/<id>/`: every directory on the path to the unit is real, every sibling is a symlink to the real content. Relative module sources and `file("../x")` still resolve; the unit's own directory is real, so the lock file lands there. Your lock file, if any, is copied in so provider versions follow it. `init` calls are serialised because Terraform's plugin cache is not safe for concurrent writers; a `TF_PLUGIN_CACHE_DIR` you have set is respected. (Installing [providers](#providers) is the one thing that does write the root's lock file, on purpose.)

If the shadow tree can't be built, `validate` reports **failed** rather than running against your repo.

## How the tools are run

- `fmt` runs per directory, not `-recursive`, since every module is its own unit; recursive would report files twice.
- "Did you mean" fixes come from Terraform's own suggestion in the diagnostic (Terraform 1.x includes it), verified against the exact range before offering the edit. There is no fallback that guesses from provider schemas.
- `tflint` runs with its working directory set to the unit, not `--chdir`: with `--chdir` it prints paths relative to its original cwd with inconsistent symlink handling.
- `ansible-playbook --syntax-check` output is parsed in both the ansible-core ≥ 2.19 format (`[ERROR]: … Origin: file:line:col`) and the older `ERROR! … The error appears to be in` format.
- `ansible-lint -f json` reports positions as `positions.begin.line` for some rules and `lines.begin` for others; both are handled.

## Editing safety

Saves are atomic (temp file + rename) and require `If-Match` with the sha you loaded; a mismatch is a 409 and nothing is written. Autosave pauses while a file has changed on disk under your edits, until you reload or overwrite. Quick-fix edits are applied to the editor buffer (never straight to disk) and only if the text at the fix's range still equals what the fix expects. Deletes need the same `If-Match`, so a file that changed since you opened it is never removed unseen. `.git/`, `.groundwork/`, `.terraform/` and `*.tfstate*` are never editable or deletable through the API.

## Testing against real tools

```
make integration                       # real terraform / tflint / yamllint / ansible-lint if installed
GW_TEST_NETWORK=1 make integration     # also runs the tests that download providers
```

The parsers are tested against captured output of the real tools in `testdata/checks/`.
