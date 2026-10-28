# Setting up a new repository with `groundwork init`

In a repository with no Terraform or Ansible, `groundwork init` sets up only the `--tools` chosen, and nothing else:
- `groundwork.yaml`, always;
- `ansible/ansible.cfg`, with Ansible;
- `ansible/inventory/tf_outputs.py`, with Ansible and Terraform or OpenTofu: the inventory script that reads the `ansible_hosts` output.

The user writes the Terraform (any provider) and the playbooks. It never overwrites an existing file. Nothing touches the cloud.

**Always preview first and show the user the file list:**

```sh
groundwork init --project acme --tools terraform,ansible --dry-run
groundwork init --project acme --tools terraform,ansible --yes   # after the user agrees
```

## Flags

| Flag | Default | |
|---|---|---|
| `--project` | directory name | lowercase, digits, dashes |
| `--tools terraform,ansible` | `terraform,ansible` (`tofu,ansible` when only tofu is installed) | `terraform` or `tofu` (not both), and/or `ansible`. Ask the user which they use |
| `--envs dev,staging,prod` | `dev,staging,prod` | Terraform/OpenTofu only. `prod` requires approval in the console |
| `--layout dirs\|workspaces` | `dirs` | where the Terraform roots will live: `terraform/envs/<env>`, or `terraform/` with a workspace per environment |
| `--checks fmt,validate,tflint,ansible-lint,yamllint` | that list | also `checkov` |
| `--vault-password-file ~/.vault-pass-acme` | none | Ansible only; must be outside the repository. If it doesn't exist, init asks for the password on the terminal (hidden, twice) and creates it with mode 600. Without it, the user types the password in the console (Variables → Secrets → Unlock) |
| `--git-commit` | off | commit the files written |
| `--detect` | off | only detect existing code; never set up |

Invalid flags are reported together. Fix them all and re-run. Never ask for the vault password yourself, and don't pass `--vault-password-file` for a file that doesn't exist unless the user will type the password.

## After setup

1. The user adds an `ansible_hosts` output to their Terraform (see "Passing data from Terraform to Ansible" in SKILL.md).
2. `groundwork check --json`: roots that don't exist yet show as `skipped`, not failed.
3. `groundwork doctor`: shows missing tools, with install commands that use an installer on this machine.
