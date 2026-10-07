# Variables and secrets

The **Variables** screen answers "what value does this get in each environment, and where is it set?" without running anything.

## Terraform

Pick a **stack**: one configuration across its environments. A root with workspace environments (`envs:` in `groundwork.yaml`) is one stack. Directory-per-environment roots group by their path with the environment segment replaced by `*`: `envs/dev` and `envs/prod` form the stack `envs/*`.

Each cell shows the value a plan would use and where it came from. Terraform's documented order, lowest to highest:

1. `default` in the `variable` block
2. `TF_VAR_<name>` in groundwork's environment (shown masked, as "environment")
3. `terraform.tfvars`, then `terraform.tfvars.json`
4. `*.auto.tfvars` / `*.auto.tfvars.json`, in lexical order
5. `-var-file` files configured for the environment, in order

Rows are flagged when the value **differs** between environments, is **missing** (required, no value anywhere: the plan will fail), or is **unused** (declared, never referenced). Values of `sensitive = true` variables are masked. Values set with `-var` on a command line aren't shown: groundwork doesn't pass any.

The matrix agrees with `terraform console`; an integration test checks that against real Terraform.

**Editing a value.** Select a cell, type the new value as an HCL literal (`"text"`, `3`, `true`, `["a", "b"]`, `{ team = "core" }`) and pick the file to write: the environment's `-var-file`, `terraform.tfvars` or an existing `*.auto.tfvars`. Preview shows the diff; saving writes it with hclwrite (comments and layout kept) only if the file hasn't changed since the preview, then validates the root. groundwork refuses to write a **sensitive** variable to a tfvars file.

## Ansible

Group variables per environment, one row per group and variable. Within an environment the winner follows Ansible's order for group-level variables: inventory-file vars, then inventory `group_vars/`, then playbook `group_vars/`. Each cell links to the file and line that sets it. Host variables, and the full precedence ladder for one host, are on the [Inventory screen](ansible.md).

## Secrets

The Secrets tab lists encrypted values and credential variables. Nothing is decrypted while listing except a test decryption (output discarded) to show whether the value **can decrypt**; the result is cached per file content.

| Store | Found by | Reveal |
|---|---|---|
| SOPS | YAML/JSON files with a `sops:` block (keys are listed, they're clear text in SOPS files) | `sops --decrypt --extract '["key"]'`. Needs the `sops` CLI and your key (age, KMS, PGP) as usual. |
| ansible-vault, whole file | files starting `$ANSIBLE_VAULT;` | `ansible-vault view` |
| ansible-vault, inline | `key: !vault \|` values | `ansible-vault decrypt` of that value |
| Environment | known credential variables (`AWS_*`, `ARM_*`, `GOOGLE_*`, `VAULT_TOKEN`, …) set in groundwork's environment | never: names only |

Vault commands run in the Ansible project with its `ansible.cfg`. Set the password with `vault_password_file:` on the project in `groundwork.yaml` (relative to the project, or `~/…`); `ANSIBLE_VAULT_PASSWORD_FILE` or `vault_password_file` in `ansible.cfg` also work. States: **can decrypt**, **wrong password**, **no password** (none configured), **tool missing**.

**Reveal** decrypts one value on request. The value is sent in that one response (`Cache-Control: no-store`), shown for 30 seconds, and never written to groundwork's database, logs or cache. Each reveal is recorded in an audit list with the secret's name, file and time, never its value. Sensitive Terraform variables can be revealed from the detail panel the same way.

## Plaintext secret scan

The built-in **secrets** check (enabled by default; listed under `checks.enabled`) looks for credentials committed in clear text:

- unmistakable formats: AWS access key IDs, private keys, GitHub/GitLab/Slack tokens, Stripe live keys, Google API keys (errors);
- `password` / `secret` / `token` / `api_key` / `access_key` / `credential` keys assigned a literal that looks random enough (warnings).

References and templates (`var.x`, `{{ x }}`, `lookup(...)`), placeholders (`change-me`, `<...>`), paths, keys like `*_file` or `*_arn`, comments, and encrypted files are ignored. Findings appear on the Code screen with the other checks, and on the Secrets tab ("Scan for plaintext secrets" scans the whole repo).

**Move to vault.** For a finding in an Ansible YAML vars file (a top-level `key: value` line), groundwork runs `ansible-vault encrypt_string` with the project's vault settings and replaces the line with the `!vault` block. The preview shows the change with the old value masked; the write is refused if the file changed since. The old value is still in git history: rotate it.

**Terraform.** groundwork doesn't rewrite plaintext tfvars secrets for you. Mark the variable `sensitive = true`, remove it from the tfvars file, and pass it as `TF_VAR_<name>` in the environment you start groundwork from, or read it from SOPS with the `carlpett/sops` provider's `sops_file` data source. Then rotate it.
