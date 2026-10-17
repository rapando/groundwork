# terraform-only

A pure Terraform repo: one reusable module, two environments (`dev`, `prod`).
It uses only Terraform's built-in `terraform_data` resource, so it validates and plans offline with no cloud account.

```
modules/service/     reusable module (resource + variables + outputs)
envs/dev/            root: backend + module call + tfvars
envs/prod/
```

Try it on a copy outside this repository (inside it, groundwork would manage the whole groundwork checkout):

```
cp -R examples/terraform-only /tmp/terraform-only && cd /tmp/terraform-only && git init && groundwork
```
