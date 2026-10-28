# First run

When you import a project (by running `groundwork` in it, `groundwork add`, or the console's Projects page), groundwork uses the repository root: the nearest ancestor with `.git`, else the folder itself. Opening a project that has no `groundwork.yaml` yet shows one of these screens:

| Repo contents | Screen | CLI equivalent |
|---|---|---|
| Terraform and/or Ansible found | **Detect** (`/setup/detect`) | `groundwork init --detect` |
| Nothing recognisable | **Set up** (`/setup/scaffold`) | `groundwork init` |
| `groundwork.yaml` exists | Overview | `groundwork` |

Nothing is written until you confirm (`--yes` skips the prompt); `--dry-run` writes nothing.

## Detection

Detection never runs Terraform or Ansible. It reads files: HCL blocks (`backend`, `provider`, `required_providers`, `module` sources), `ansible.cfg`, playbook structure, inventories, `group_vars`/`host_vars`, and CI steps that call `terraform`, `tofu` or `ansible-playbook`. Each finding shows its evidence (file and line). `.gitignore`, the config's `ignore:` list and `.git`, `.terraform`, `node_modules`, `vendor`, `.venv` are skipped; scans stop at 50,000 files.

A directory referenced as `source = "./…"` by another is a **module**; one with a backend, provider or tfvars is a **root**. Environments are matched across tools by normalised name (`production` = `prod`, `stg` = `staging`, `development` = `dev`).

`mode` is `standalone` when more than 60% of files are IaC or YAML, otherwise `embedded`. In either mode the Detect flow writes only `groundwork.yaml` (plus lint configs and a `.gitignore` line if you tick them). It sets `terraform.binary` to `terraform`, or to `tofu` when only OpenTofu is installed; change it in the YAML if that's wrong.

## Setting up a new repository

In a repository with no Terraform or Ansible yet, groundwork writes its config and a provider-neutral starting point for each environment. You add the providers, resources and playbooks.

First choose the tools the repository will use (`--tools`): **Terraform** or **OpenTofu** (one of the two), **Ansible**, or both. The default is Terraform and Ansible, or OpenTofu and Ansible when only `tofu` is installed. groundwork sets up only what you choose:

| Tools | Files |
|---|---|
| Terraform or OpenTofu | `groundwork.yaml`, starter Terraform, `.gitignore` |
| Ansible | `groundwork.yaml`, `ansible/ansible.cfg` |
| both | all of the above, plus `ansible/inventory/tf_outputs.py` |

- `groundwork.yaml`: the binary (`terraform` or `tofu`), your environments and where their roots are, the Ansible project, and the checks for the chosen tools.
- Starter Terraform: `versions.tf`, `main.tf`, `variables.tf` and `outputs.tf` in each root, with a `required_version`, an `environment` variable set per environment, and a `name_prefix` local and output. No provider is chosen: add yours to `versions.tf`. With Ansible too, `outputs.tf` has an empty `ansible_hosts` output to fill in (see below). The files pass `fmt`, `validate` and tflint as written.
- `.gitignore`: `.terraform/`, state files and crash logs. An existing `.gitignore` only gets the lines it lacks.
- `ansible/ansible.cfg`: points Ansible at `ansible/inventory/`, so `ansible-playbook` finds the inventory outside groundwork too. With Ansible alone, put your own inventory there, such as `inventory/hosts.yml`.
- `ansible/inventory/tf_outputs.py`: the inventory script that turns your Terraform or OpenTofu hosts into Ansible hosts.

Environments and layout only apply to Terraform and OpenTofu, and the vault password only to Ansible. `--envs` sets the environments (default `dev,staging,prod`). `--layout` says where the Terraform roots live: `dirs`, a directory per environment under `terraform/envs/<env>/` with its own `terraform.tfvars` (the default), or `workspaces`, one root in `terraform/` with a workspace and a `terraform/env/<env>.tfvars` per environment. Existing files are never overwritten.

### The ansible_hosts output

The inventory script runs `terraform output -json ansible_hosts` (or `tofu output …`) in the environment's root. It uses the binary chosen at setup, else whichever of `terraform` and `tofu` is installed; set `GROUNDWORK_TF_BINARY` to override it. If you later change `terraform.binary` in `groundwork.yaml`, edit the script's default too. Add that output to your Terraform: a map of group to a list of hosts.

```hcl
output "ansible_hosts" {
  value = {
    web = [for s in vultr_instance.web : { name = s.label, ip = s.main_ip }]
    db  = [{ name = "db-1", ip = vultr_instance.db.internal_ip, ansible_user = "root" }]
  }
}
```

Each host needs a `name`. `ip` becomes `ansible_host`, and every other field (`ansible_user`, `ansible_port`, `ansible_connection`, or your own variables) is passed through as a host variable. Select the environment with `GROUNDWORK_ENV`.

### Passing a generated password to Ansible

Because extra fields become host variables, a value Terraform generates, such as a database password, reaches Ansible through the same output:

```hcl
resource "random_password" "db" {
  length  = 32
  special = false
}

output "ansible_hosts" {
  sensitive = true
  value = {
    db  = [{ name = "db-1", ip = vultr_instance.db.internal_ip, db_password = random_password.db.result }]
    web = [{ name = "web-1", ip = vultr_instance.web.main_ip, db_password = random_password.db.result }]
  }
}
```

Add `hashicorp/random` to `required_providers` in `versions.tf`. Then use it in a playbook like any other variable:

```yaml
- community.postgresql.postgresql_user:
    name: app
    password: "{{ db_password }}"
  no_log: true
```

- The output must be `sensitive = true` once it holds a sensitive value, or the plan fails. `output -json` still prints it, so the script works.
- It's a host variable, so set it on every host that needs it (here the database and the web servers that connect to it).
- The password is in the Terraform state in plain text, as with any `random_password`: keep the state backend private.
- groundwork's [host variables view](ansible.md) masks it only if its name looks secret (contains `pass`, `secret`, `token`, …). Run logs don't mask it: use `no_log: true` on tasks that could print it.
- Ansible sees it only after an apply, since the script reads the current state.

### The ansible-vault password

Setup asks for the vault password before it writes anything, and **Create files** stays disabled until you've typed it twice.

- **Type it in groundwork** (the default): groundwork unlocks the vault with it straight away and keeps it in memory, never on disk, until you lock the vault or the service stops. After a restart, unlock it again on [Variables → Secrets](variables.md#typing-the-vault-password).
- **Password file** (`--vault-password-file ~/.vault-pass-<project>`): groundwork creates the file with the password on one line, mode `600`, and sets `vault_password_file` in `groundwork.yaml`. The file must be outside the repository so it can't be committed. If it already exists, groundwork uses it as it is and doesn't ask for a password. `groundwork init` asks for the password on the terminal, hidden.

When setup is done, **Add a secret** opens Variables → Secrets → New secret. Put a secret in `ansible/group_vars/<env>/vault.yml` for one environment, or `ansible/group_vars/all/vault.yml` for every host.

### Preview and tools

The preview is rendered in memory. **Create files** never overwrites an existing file unless you tick it. **Download as zip** produces the same files without touching the repo; the password file is never in it.

Missing tools are listed with install commands that use an installer found on the machine (Homebrew, pipx, uv or Go), with the other ways to install them under **Other ways to install**. groundwork never runs a package manager.

## Code and checks

Once configured, the **Code** screen shows a file tree (IaC-only or all files, filterable), an editor with inline diagnostics and one-click fixes, a problems tray, and a git diff view. **+** above the tree creates a file (folders are created as needed) and **Delete** removes the open one after a confirmation. See [checks](checks.md).

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

For UI work, run `groundwork serve --no-open --port 7420 <repo>`, sign in at the printed URL once, then `cd web && npm run dev` and open `/p/<project id>/` on the dev server.
