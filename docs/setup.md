# First run

When you import a project (by running `groundwork` in it, `groundwork add`, or the console's Projects page), groundwork uses the repository root: the nearest ancestor with `.git`, else the folder itself. Opening a project that has no `groundwork.yaml` yet shows one of these screens:

| Repo contents | Screen | CLI equivalent |
|---|---|---|
| Terraform and/or Ansible found | **Detect** (`/setup/detect`) | `groundwork init --detect` |
| Nothing recognisable | **Scaffold** (`/setup/scaffold`) | `groundwork init --preset <preset>` |
| `groundwork.yaml` exists | Overview | `groundwork` |

Nothing is written until you confirm (`--yes` skips the prompt); `--dry-run` writes nothing.

## Detection

Detection never runs Terraform or Ansible. It reads files: HCL blocks (`backend`, `provider`, `required_providers`, `module` sources), `ansible.cfg`, playbook structure, inventories, `group_vars`/`host_vars`, and CI steps that call `terraform`, `tofu` or `ansible-playbook`. Each finding shows its evidence (file and line). `.gitignore`, the config's `ignore:` list and `.git`, `.terraform`, `node_modules`, `vendor`, `.venv` are skipped; scans stop at 50,000 files.

A directory referenced as `source = "./…"` by another is a **module**; one with a backend, provider or tfvars is a **root**. Environments are matched across tools by normalised name (`production` = `prod`, `stg` = `staging`, `development` = `dev`).

`mode` is `standalone` when more than 60% of files are IaC or YAML, otherwise `embedded`. In either mode the Detect flow writes only `groundwork.yaml` (plus lint configs and a `.gitignore` line if you tick them).

## Scaffolding

A scaffold is a Terraform layout with a `network` module, an Ansible project (`ansible.cfg`, `playbooks/site.yml`, starter roles from `--roles`, `group_vars` per environment), lint configs for the checks you pick, a `.pre-commit-config.yaml` and a GitHub Actions plan workflow (`--no-pre-commit` and `--no-ci` leave those two out). `--envs` sets the environments (default `dev,staging,prod`).

| Preset | Provider and credentials | State (`--backend`) | `--region` |
|---|---|---|---|
| `aws-envs` | AWS | `s3` (default) with a DynamoDB lock table `<bucket>-lock`; `local`, `http` | an AWS region, e.g. `eu-west-1` |
| `gcp-envs` | Google Cloud; pass `--gcp-project` | `gcs` (default); `local`, `http` | a GCP region, e.g. `europe-west1` |
| `vultr` | `vultr/vultr`, token in `VULTR_API_KEY` | `local` (default), `http`, `s3` | e.g. `ams` |
| `digitalocean` | `digitalocean/digitalocean`, token in `DIGITALOCEAN_TOKEN` | `local` (default), `http`, `s3` | e.g. `fra1` |
| `hetzner` | `hetznercloud/hcloud`, token in `HCLOUD_TOKEN` | `local` (default), `http`, `s3` | a network zone, e.g. `eu-central` |
| `linode` | `linode/linode`, token in `LINODE_TOKEN` | `local` (default), `http`, `s3` | e.g. `eu-central` |
| `docker` | `kreuzwerker/docker`, against the local daemon or `DOCKER_HOST` | `local` (default), `http` | not used |
| `onprem` | none: add your own provider block | `local` (default), `http` | not used |

`--bucket` names the state bucket (default `<project>-tfstate`). `groundwork doctor` (and the Troubleshoot screen) checks each provider's credentials: the token variable for the token-based providers, the Docker daemon for `docker`. Token variables must be in the service's environment, which the login service doesn't take from your shell ([the service](service.md#starting-it)).

- **S3-compatible state** for Vultr, DigitalOcean, Hetzner and Linode: `--backend s3 --s3-endpoint <url>` stores state in the provider's object storage (e.g. `https://fra1.digitaloceanspaces.com`), with its access keys in `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. Hetzner gets `use_lockfile = true` and requires Terraform 1.10; the others require 1.6 and leave it commented out: Vultr's object storage doesn't support locking, and DigitalOcean's and Linode's support for the conditional writes it needs isn't confirmed. Without a lock, don't run two applies at once.
- **Network module**: a VPC network on AWS and Google Cloud, a private network on the token-based providers, a Docker network for `docker`. `onprem` has none.
- **Docker**: a starter `web` container whose published ports are the `web_ports` variable, bound to `127.0.0.1` on port 8081 for the first environment, 8082 for the second, and so on.
- **Other providers** (Azure, for example) start from `onprem`. Adding another token-based preset to groundwork is a row in `internal/scaffold/clouds.go` plus `network_main.tf.tmpl` and `network_outputs.tf.tmpl` under `internal/scaffold/presets/cloud/<preset>/`.

Layouts (`--layout`): `dirs`, a directory per environment (the default), or `workspaces`, one root with a workspace per environment.

The Ansible inventory (`--inventory`) is either a static YAML file per environment or, by default, `ansible/inventory/tf_outputs.py`, which reads the Terraform `ansible_hosts` output: a map of group to a list of hosts. Each host needs a `name`. `ip` becomes `ansible_host`, and every other field (`ansible_user`, `ansible_port`, `ansible_connection`, or your own variables) is passed through as a host variable. Select the environment with `GROUNDWORK_ENV`.

The preview is rendered in memory. **Create files** never overwrites an existing file unless you tick it, and `.gitignore` is merged, not replaced. **Download as zip** produces the same files without touching the repo. Missing tools are only listed with install commands; groundwork never runs a package manager.

The Ansible vault option (`--vault`) writes `vault.yml.example` files only. groundwork never writes secrets.

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

For UI work, run `groundwork serve --no-open --port 7420 <repo>`, sign in at the printed URL once, then `cd web && npm run dev` and open `/p/<project id>/` on the dev server.
