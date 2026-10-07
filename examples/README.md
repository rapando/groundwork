# Examples

Three small repos to point groundwork at. Each has its own README and validates offline (no cloud account needed).

| Example | What it is | groundwork sees it as |
|---|---|---|
| [terraform-only](terraform-only) | A Terraform module and two environments | standalone repo, 2 roots + 1 module |
| [ansible-only](ansible-only) | One role, a playbook, dev/prod inventories | standalone repo, 1 Ansible project |
| [app-with-infra](app-with-infra) | A small Go service with `deploy/terraform` and `ops/ansible` | embedded repo, Terraform root + Ansible project |

```
cp -R examples/app-with-infra /tmp/demo && cd /tmp/demo && git init && groundwork
```

Copy an example out of this repo before running groundwork in it: groundwork creates `.groundwork/` and `groundwork.yaml` in the repo it manages.
