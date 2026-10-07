# First run

`groundwork` looks at the repository it is started in (the nearest ancestor with `.git`, else the current directory).

| Repo contents | Screen | CLI equivalent |
|---|---|---|
| Terraform and/or Ansible found | **Detect** (`/setup/detect`) | `groundwork init --detect` |
| Nothing recognisable | **Scaffold** (`/setup/scaffold`) | `groundwork init --preset aws-envs` |
| `groundwork.yaml` exists | Overview | `groundwork` |

Nothing is written until you confirm. `--dry-run` writes nothing.

## Detection

Detection never runs Terraform or Ansible. It reads files: HCL blocks (`backend`, `provider`, `required_providers`, `module` sources), `ansible.cfg`, playbook structure, inventories, `group_vars`/`host_vars`, and CI steps that call `terraform`, `tofu` or `ansible-playbook`.
Each finding shows its evidence (file and line). `.gitignore`, the config's `ignore:` list and `.git`, `.terraform`, `node_modules`, `vendor`, `.venv` are skipped; scans stop at 50,000 files.

A directory referenced as `source = "./…"` by another is a **module**; one with a backend, provider or tfvars is a **root**.
Environments are matched across tools by normalised name (`production` = `prod`, `stg` = `staging`, `development` = `dev`).

`mode` is `standalone` when more than 60% of files are IaC or YAML, otherwise `embedded`.
In either mode the Detect flow writes only `groundwork.yaml` (plus lint configs and a `.gitignore` line if you tick them).

## Scaffolding

Presets: `aws-envs`, `onprem`. Layouts: a directory per environment, or one root with workspaces.
The preview is rendered in memory; **Create files** never overwrites an existing file unless you tick it, and `.gitignore` is merged, not replaced.
`Download as zip` produces the same files without touching the repo. Missing tools are only listed with install commands; groundwork never runs a package manager.

The Ansible vault option writes `vault.yml.example` files only. groundwork never writes secrets.

## Code and checks

Once configured, the **Code** screen shows a file tree (IaC-only or all files, filterable), an editor with inline diagnostics and one-click fixes, a problems tray, and a git diff view. See [checks](checks.md).

## Runs

The Overview lists each environment with a **Plan** button; plans, approvals and applies are described in [runs](runs.md).

## Development

```
make web      # build the SPA into internal/server/dist (committed)
make test     # Go + web unit tests
make e2e      # Playwright against installed Chrome
make integration  # real terraform/linters, see docs/checks.md
make golden   # accept new detection golden files
```

For UI work, run `groundwork serve --no-open --port 7420`, open the printed URL once, then `cd web && npm run dev`.
