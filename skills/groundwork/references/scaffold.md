# Scaffolding a repository with `groundwork init --preset`

`init --preset <name>` writes a complete starter layout:
- Terraform roots per environment, plus a network module;
- an Ansible project with roles and inventory;
- `groundwork.yaml`, lint configs, a pre-commit config and a GitHub Actions plan workflow.

It never overwrites an existing file. Existing files are listed and kept, and `.gitignore` is merged. Nothing touches the cloud.

**Always preview first and show the user the file list:**

```sh
groundwork init --preset aws-envs --region eu-west-1 --dry-run
groundwork init --preset aws-envs --region eu-west-1 --yes          # after the user agrees
```

**Choose `--project` deliberately.** It defaults to the directory name, and it prefixes every resource name and the default state bucket (`<project>-tfstate`). S3, GCS and most S3-compatible stores have global bucket names, so a generic name like `repo-tfstate` or `infra-tfstate` is probably taken. Use the company or product name the user gave you, or ask for one.

## Presets

| Preset | Required flags | State backends (`--backend`) | Credentials at plan time |
|---|---|---|---|
| `aws-envs` | `--region eu-west-1` | `s3` (default; bucket + DynamoDB lock), `local`, `http` | AWS profile, SSO or keys |
| `gcp-envs` | `--gcp-project acme-prod-123 --region europe-west1` | `gcs` (default), `local`, `http` | `gcloud auth application-default login` |
| `vultr` | `--region ams` | `local` (default), `http`, `s3` + `--s3-endpoint https://ams1.vultrobjects.com` | `VULTR_API_KEY` |
| `digitalocean` | `--region fra1` | `local`, `http`, `s3` + `--s3-endpoint https://fra1.digitaloceanspaces.com` | `DIGITALOCEAN_TOKEN` |
| `hetzner` | `--region eu-central` (a network zone) | `local`, `http`, `s3` + `--s3-endpoint https://fsn1.your-objectstorage.com` | `HCLOUD_TOKEN` |
| `linode` | `--region eu-central` | `local`, `http`, `s3` + `--s3-endpoint https://eu-central-1.linodeobjects.com` | `LINODE_TOKEN` |
| `docker` | none | `local`, `http` | the Docker daemon, or `DOCKER_HOST` |
| `onprem` | none | `local`, `http` | none (no cloud provider; hosts are a variable) |

For `s3` on the token providers, Terraform reads the object storage's access keys from `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`.
- **Hetzner** gets native state locking.
- **Vultr, DigitalOcean and Linode** leave `use_lockfile` commented, because conditional writes aren't confirmed there.

`--bucket` defaults to `<project>-tfstate`.

**Docker** creates a network and a `web` container (`nginx:stable-alpine`). Its published ports come from the `web_ports` variable, bound to `127.0.0.1` on port 8080 plus the environment's position.

## Other flags

| Flag | Default | |
|---|---|---|
| `--project` | directory name | used in resource names; lowercase, digits, dashes |
| `--envs dev,staging,prod` | `dev,staging,prod` | `prod` requires approval in the console |
| `--layout dirs\|workspaces` | `dirs` | a directory per environment, or one root with a workspace per environment |
| `--inventory terraform\|static` | `terraform` | Ansible inventory from the `ansible_hosts` output, or static YAML per environment |
| `--roles common,nginx` | `common` | starter Ansible roles |
| `--checks fmt,validate,tflint,ansible-lint,yamllint` | that list | also `checkov` |
| `--vault` | off | adds `vault.yml.example` files (never real secrets) |
| `--no-pre-commit`, `--no-ci` | | skip `.pre-commit-config.yaml` / `.github/workflows/plan.yml` |
| `--git-commit` | off | commit the scaffolded files |

Invalid flags are reported together (`--region: enter a Vultr region such as ams`). Fix them all and re-run.

## After scaffolding

1. `groundwork check --json`: a fresh scaffold is expected to be clean. Tools that aren't installed show as `skipped`.
2. `groundwork doctor`: shows which credentials are missing before anyone plans.
3. Hand off to the console (`groundwork --no-open`) for the first plan.

## Without a preset: adopting an existing repository

```sh
groundwork init --dry-run      # detection: prints the groundwork.yaml it would write, with evidence
groundwork init --yes          # writes only groundwork.yaml
```

Detection reads files only and never runs Terraform or Ansible:
- HCL backend, provider and module blocks;
- `ansible.cfg`, playbooks, inventories, `group_vars` and `host_vars`;
- CI steps that call terraform or ansible.

Review the detected roots and environments with the user before writing.
