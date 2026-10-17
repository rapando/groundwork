---
name: groundwork
description: Drive groundwork, the local console for Terraform and Ansible, from the command line. It can validate infrastructure code (groundwork check --json), diagnose tools, credentials and state backends (groundwork doctor --json), detect drift, scaffold a new Terraform + Ansible repository for AWS, GCP, Vultr, DigitalOcean, Hetzner, Linode, Docker or on-prem, and hand plans, applies and ansible-vault secrets to a human in the console. Use this skill whenever a repository has a groundwork.yaml, whenever the user mentions groundwork, and whenever you are editing, validating, linting or troubleshooting Terraform/OpenTofu or Ansible code on a machine where the `groundwork` CLI is installed, even if the user doesn't name it. Also use it when asked to set up or scaffold an infrastructure repo, check for drift, add or change an ansible-vault secret, or figure out why terraform/ansible "doesn't work here".
---

# groundwork

groundwork is a local web console for Terraform (or OpenTofu) and Ansible. One background service on the user's machine hosts their infrastructure repositories as *projects*. The service checks code as it changes, shows plans that a person reviews and approves, and explains failures. The `groundwork` CLI is how you, an agent, work with it.

## Know the boundary first

**groundwork deliberately has no command that plans and applies.** Terraform plans, applies, destroys and Ansible playbook runs happen in the console. There, a person reads the plan, sees dangerous changes flagged, and applies it. Production-like environments also need an explicit approval of the exact plan. This is the product's core promise, so keep it intact:

- Don't look for a way around approval. Don't call the HTTP API with the CLI token, and don't set `approval: none` in `groundwork.yaml` to skip the review step. Don't run `terraform apply` or `ansible-playbook` yourself as a substitute.
  - If the user explicitly asks you to run terraform directly, that's their call. It then happens outside groundwork, so say so.
- When work reaches the "now apply it" stage, hand off. Give the user the project's console URL (see *Handing off* below) and tell them what to review.
- Everything below is read-only or local file generation, except `groundwork drift`. It runs refresh-only plans against real cloud APIs with the user's credentials. It changes nothing, but it is slow and touches real accounts, so run it when the task calls for it rather than reflexively.

## Is it set up here?

```sh
groundwork version            # installed? (exit 127 / "command not found" = no)
ls groundwork.yaml            # is this repository configured?
```

| Situation | Do this |
|---|---|
| `groundwork` not installed | Tell the user. Install: `curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh \| sh`, or `go install github.com/rapando/groundwork/cmd/groundwork@latest`. Don't install software without asking. |
| `groundwork.yaml` exists | Go straight to `check`, `doctor`, `drift`. |
| Terraform/Ansible code but no `groundwork.yaml` | `groundwork init --dry-run` shows the detected config; with the user's OK, `groundwork init --yes` writes only `groundwork.yaml`. |
| Empty repository, or the user wants a new layout | Scaffold with a preset: see [references/scaffold.md](references/scaffold.md). |

`init`, `check`, `doctor` and `drift` work directly in the repository, without the background service. They also work in CI.

## Validate code: `groundwork check`

This is the command you'll use most. It runs every configured check once (terraform fmt and validate, tflint, checkov, yamllint, ansible-lint, ansible syntax-check, and a built-in secret scanner) over every Terraform root, module and Ansible project in `groundwork.yaml`.

```sh
groundwork check --json                         # everything
groundwork check --json --unit tf:envs/dev      # one unit (ids are in the output's "units")
groundwork check --fail-on warning              # make warnings fail too
```

Exit codes: **0** clean · **1** findings at or above `--fail-on` (default `error`) · **2** config problem (no `groundwork.yaml`, unknown unit) · **3** a tool couldn't run.

The JSON has `counts`, `units` (each with per-tool `results`: `status` is `ok`, `failed` or `skipped`, with a `message` saying why) and `diagnostics`:

```json
{"unit": "tf:envs/dev", "tool": "validate", "severity": "error",
 "message": "Reference to undeclared input variable",
 "detail": "An input variable with the name \"nope\" has not been declared…",
 "file": "envs/dev/main.tf", "line": 16, "col": 20,
 "fix": {"title": "Format file", "kind": "fmt", "file": "envs/dev/main.tf"}}
```

Work in a loop:
1. Make your edit.
2. Run `check --json`, ideally `--unit` for the part you touched; it's faster.
3. Fix each diagnostic at its `file:line`, then re-run until it's clean.

A `fix` object is a hint at the mechanical fix, e.g. `kind: fmt` means run `terraform fmt <dir>`. A tool reported as `skipped` usually isn't installed; mention it rather than treating the code as verified by that tool. Paths are relative to the repository root.

Useful one-liners:

```sh
groundwork check --json | jq -r '.diagnostics[] | "\(.file):\(.line): \(.severity) [\(.tool)] \(.message)"'
groundwork check --json | jq '.units[] | {id, results: [.results[] | select(.status != "ok")]}'
```

## Diagnose the environment: `groundwork doctor`

When terraform or ansible fails for reasons that aren't in the code, run doctor:
- "provider not found", "no credentials", "state locked", "can't reach the bucket";
- or it works in one shell but not in the console.

```sh
groundwork doctor --json      # exit 1 if any check fails
```

Each entry in `checks` has `id`, `name`, `status` (`ok` | `warn` | `fail`), `detail` and often a `hint` (the command that fixes it). It covers:
- installed tools and their versions;
- cloud credentials: AWS (including SSO expiry), Google Cloud, Azure, the Vultr/DigitalOcean/Hetzner/Linode API token environment variables, and the Docker daemon;
- state backends, including S3-compatible buckets at their own endpoint, and whether state locking is configured;
- the SSH agent, inventories, the vault password, disk space and git.

Report `fail` and `warn` entries with their hints; don't try to fix credentials yourself.

`doctor` run from your shell checks *your shell's* environment, but the console's runs use the *service's* environment. A service started at login receives only `PATH`. So when doctor passes here but a run in the console fails with missing credentials (`HCLOUD_TOKEN`, `TF_VAR_*`, `AWS_PROFILE`), that's the reason: the user has to make those variables available to the service.

## Drift: `groundwork drift`

```sh
groundwork drift                 # every Terraform root and environment
groundwork drift --env prod      # one environment
```

Runs `terraform plan -refresh-only` for each environment and lists resources changed outside Terraform. Exit **0** no drift · **1** drift found · **2** unknown environment · **3** a check failed (often credentials: run `doctor`). It never changes infrastructure or state. Reconciling drift is a plan and apply, so it goes through the console.

## Handing off to the console

The console is where the user plans, approves, applies, runs playbooks and browses graphs, inventory and secrets. To send them there:

```sh
groundwork --no-open             # in the repository: starts the service if needed, imports this repo, prints its URL
groundwork open <name> --no-open # URL of an already-imported project
groundwork projects              # imported projects and their status
groundwork add <folder | git URL>    # import more repositories (a URL is cloned)
```

Use `--no-open` so you print the URL instead of popping a browser in the middle of your work, unless the user asked you to open it. Then tell them what to do there. For example: "Open the dev environment, click **Plan**, check that only `aws_s3_bucket.logs` is created, then approve."

Leave `groundwork service install|uninstall|stop|restart` and `groundwork auth reset` to the user. They change login items, stop runs in progress, or sign every browser out.

## Editing `groundwork.yaml`

Adding a Terraform root, an environment, an Ansible inventory or a check means editing `groundwork.yaml`. The keys and their meaning are in [references/groundwork-yaml.md](references/groundwork-yaml.md). After editing, run `groundwork check` to see that the config parses (exit 2 means it doesn't, with the reason).

Some keys protect the user. Don't change these unless the user asks, and say what the change does:
- `approval` (`none` removes the approval step, even for prod);
- `terraform.danger` (resources whose destruction needs an explicit acknowledgement);
- `ansible.allow_vault_script`.

## Secrets and ansible-vault

`check` includes a `secrets` scanner that flags credentials committed in plain text. Don't silence a finding by moving the value somewhere else in plain text. For an Ansible vars file, the value belongs in ansible-vault. For Terraform, mark the variable `sensitive = true`, remove it from the tfvars file, and have the user pass it as `TF_VAR_<name>` in the service's environment. Either way the old value is in git history, so tell the user to rotate it.

Creating, editing, encrypting and rekeying ansible-vault data happens in the console, on **Variables → Secrets** (the project URL plus `variables?tab=secrets`). There the plaintext never touches the disk and opening a value is audited. So that a secret never passes through you:
- Don't decrypt vault data yourself (`ansible-vault view`, `decrypt`, `edit`), and never decrypt a file in place: that leaves the secret unencrypted in the working tree.
- Don't ask the user to paste a secret into the chat. Send them to the console: **New secret** adds an encrypted variable or file, **Edit** changes one, and **Move to vault** (on a scan finding) encrypts a plaintext value in place.
- Leave **Rekey** to the user: it changes the password everyone uses.
- You can write the code that *uses* a secret: reference `{{ db_password }}` in a template or task, and tell the user which variable to add, in which vars file, as an encrypted value.

The project's `vault_password_file` in `groundwork.yaml` (or `vault_identity_list` in `ansible.cfg`) tells groundwork how to open the vault. `doctor` reports whether it's set up.

## Passing data from Terraform to Ansible

With the Terraform inventory (the scaffold default), Ansible's inventory comes from a Terraform output named `ansible_hosts`: a map of group → list of hosts.
- Each host needs a `name`; `ip` becomes `ansible_host`.
- **Any other field becomes an Ansible host variable as is** (`ansible_user`, `ansible_port`, `ansible_connection`, or your own such as `db_host`). Prefer this over writing files from Terraform for Ansible to read.

```hcl
output "ansible_hosts" {
  value = {
    web = [{ name = "web-1", ip = aws_instance.web.private_ip, ansible_user = "ubuntu" }]
  }
}
```
