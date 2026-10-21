# `groundwork.yaml` reference

One file at the repository root, written by `groundwork init` and safe to edit by hand. groundwork validates it like request input: a bad value is a clear error (`groundwork check` exits 2 and names the line), never a silent default.

```yaml
version: 1
mode: standalone            # standalone (an IaC repo) | embedded (IaC in a subfolder of an app repo)

terraform:
  binary: terraform         # terraform | tofu
  roots:
    # a directory per environment
    - path: terraform/envs/dev
      env: dev
    - path: terraform/envs/prod
      env: prod
      approval: required    # required | none; default: required for prod, production, prd, live
    # or one root with workspaces
    - path: terraform
      envs:
        dev:  { workspace: dev,  var_files: [env/dev.tfvars] }
        prod: { workspace: prod, var_files: [env/prod.tfvars], approval: required }
  modules: [terraform/modules/*]   # globs; checked, never planned
  danger: ["*_cache_cluster"]      # extra resource types whose destruction needs a typed acknowledgement
  plan_ttl: 1h              # how long a plan stays approvable
  run_timeout: 2h           # a plan or apply is stopped after this

ansible:
  projects:
    - path: ansible
      config: ansible.cfg
      inventories:          # env -> path relative to the project (omit to use ansible.cfg's)
        dev: inventory/dev.yml
      vault_password_file: ~/.vault-pass   # a plain file; executables are refused unless allow_vault_script.
                                           # It's the "default" vault ID; more IDs come from vault_identity_list in ansible.cfg
  allow_vault_script: false
  run_timeout: 2h

checks:
  enabled: [fmt, validate, tflint, checkov, ansible-lint, yamllint, syntax-check, secrets]
  on_save: true             # re-check files when they change while the service hosts the project

drift:
  schedule: "0 7 * * 1-5"   # cron; omit for no scheduled drift checks
  notify: desktop           # desktop | webhook | none (legacy; notify.drift below takes over when set)
  webhook: https://hooks.example.com/…

notify:                     # optional; who hears about runs. Channels: console | desktop | webhook
  failed: [console, desktop, webhook]
  approval: [console, desktop]
  success: [console]        # [] = record only
  drift: [console, desktop, webhook]
  quiet_under: 30s          # successes shorter than this aren't announced
  envs: [prod]              # optional: desktop + webhook only for these environments
  webhook: https://hooks.example.com/…   # http(s), no credentials; falls back to drift.webhook

retention:                  # run history kept per project (defaults: 500 runs, 30 days)
  runs: 500
  days: 30

ignore: [vendor/, node_modules/, "**/.terraform/"]
```

## Things to know when editing

- **Environment and workspace names** are lowercase letters, digits and dashes. They end up in `terraform workspace select`, URLs and inventory lookups.
- **Paths** are relative to the repository root, except Ansible inventory paths, which are relative to the Ansible project.
- **A new Terraform directory isn't picked up until it's listed** under `roots` (planned) or matched by `modules` (checked only).
- **`approval`**: environments named `prod`, `production`, `prd` or `live` need an explicit approval of each plan by default; others are applied from the console without one. `approval: required` adds the step to any environment, and `approval: none` removes it. Only set `none` when the user explicitly asks, and say what it does.
- **`checks.enabled`** must name known checks. A check whose tool isn't installed is skipped with a hint, not failed.
- **Verify your edits:** after editing, run `groundwork check` to confirm the file parses and the units are the ones you expect (`check --json | jq '.units[].id'`).

The full documentation is in the repository's `docs/setup.md`, `docs/checks.md`, `docs/runs.md` and `docs/ansible.md`.
