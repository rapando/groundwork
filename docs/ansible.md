# Ansible

groundwork runs `ansible`, `ansible-playbook` and `ansible-inventory` from your PATH, in the project directory (so your `ansible.cfg` applies), with `ANSIBLE_HOME` under `.groundwork/`.

## Inventory screen

Pick an environment (one per entry in `inventories:` for the project in `groundwork.yaml`). For each host: address, OS (from cached facts), reachability (from the last ping), and the outcome of the last playbook run. Groups show host counts and how many are down; the files that make up the inventory (`inventory/*.yml`, `group_vars/`, `host_vars/`) link to the editor.

**Variables on a host, winner first.** Ansible doesn't say where a value came from, so groundwork resolves it itself, following Ansible's documented precedence (lowest to highest):

1. role defaults (`roles/*/defaults/main.yml`)
2. group vars in the inventory file (`all` first, then by group depth)
3. inventory `group_vars/all`
4. playbook `group_vars/all`
5. inventory `group_vars/<group>`
6. playbook `group_vars/<group>`
7. host vars in the inventory file
8. inventory `host_vars/<host>`
9. playbook `host_vars/<host>`

Both `group_vars/web.yml` and `group_vars/web/*.yml` forms are read; YAML and INI inventories are understood. The winning value is then compared with `ansible-inventory --host`. If they disagree, or Ansible reports a variable groundwork can't place (dynamic inventory plugins, scripts), the row says **can't determine source** and shows Ansible's value rather than guessing. Variables set only in role defaults are marked as such: they apply in plays that use the role.

Values of secret-looking variables (`*password*`, `*token*`, `*secret*`, `*key*`) are masked; `!vault` values show as encrypted; vault-encrypted files are listed, not opened: reveal them from the [Variables screen](variables.md#secrets).

## Editing the inventory

When an environment's inventory is a static YAML file in the repository, the Inventory screen edits it:

- **Hosts**: *Add host* (into a group, optionally with an `ansible_host` address); on a host, *Add to group…* and *Remove…* (from one group or the whole inventory). Moving a host is adding it to the new group and removing it from the old.
- **Groups**: *+ Add* next to Groups (inside `all` or any group); an empty group shows *Remove group*. Groups with hosts, child groups or variables can't be removed until they're empty. Empty groups are listed even though `ansible-inventory` leaves them out.
- **Variables**: *+ Add variable* on a host, and *Edit*/*Remove* on any variable whose value comes from a file groundwork can edit. A variable applies to the host or to one of its groups, and is written to the inventory file itself or to a `host_vars`/`group_vars` file: an existing one (next to the inventory first, then the playbook directory), or a new `<inventory dir>/host_vars/<host>.yml` when there's none. Values are typed as YAML (`8080`, `"text"`, `[a, b]`).

Every change is shown as a diff first and written only when you press *Apply*; if the file changed in between, nothing is written and you're asked to make the change again. Edits splice only the lines they touch, so comments, blank lines, quoting and key order stay as you wrote them. Saving runs the file's checks like any other save.

Not edited here: INI inventories, inventory directories and dynamic inventories (edit their files in Code), vault-encrypted files (use `ansible-vault edit`), flow-style mappings like `{a: 1}` with entries, and secret-looking variables, which are refused so they don't land in plain text: keep them in ansible-vault ([Variables → Secrets](variables.md#secrets) can move a value there).

## Jobs

| Kind | What runs | Approval |
|---|---|---|
| ping | `ansible <hosts> -m ping` | none; updates reachability |
| gather facts | `ansible <hosts> -m setup -a gather_subset=min` | none; caches a small set of facts (OS, kernel, CPU/memory, Python, address). `ansible_env` is never kept. |
| dry run | `ansible-playbook --syntax-check`, then `--check --diff` | none |
| playbook | dry run, then **waits for approval**, then the real run with `--diff` | always; prod-like environments need the name typed |
| ad-hoc | `ansible <pattern> -m <module> -a <args>` | read-only modules (`ping`, `setup`, `stat`, `debug`, `*_facts`) need none; anything else needs the environment name typed, recorded as an approval |

A playbook's dry run is its "plan": approving it is refused if the project's files changed since the dry run, or if the dry run is older than `terraform.plan_ttl` (default 1h); the check is repeated under the job lock right before the real run. A dry run with failed or unreachable hosts can still be approved (check mode often trips over tasks that depend on earlier changes), and the failures are shown for review.

Host patterns, `--limit`, `--tags` and module names are validated (no whitespace, no leading `-`), passed as `--flag=value`, and positional arguments follow `--`, so nothing can be read as an extra option.

## Live results

An embedded callback plugin (`internal/ansible/callback/groundwork.py`) streams play, task and per-host results as JSON on file descriptor 3, alongside Ansible's normal output (which is the run's log). The Run screen shows each host's worst outcome, its per-task results, failure messages and diffs, for the dry run and the real run separately. Messages and diffs are redacted like every log line, and results of `no_log` tasks are hidden. It sets `ANSIBLE_CALLBACKS_ENABLED=groundwork`, which replaces any `callbacks_enabled` from your `ansible.cfg` for groundwork's runs.

## Testing

Tests run real Ansible (skipped when it isn't installed) against `testdata/repos/ansible-lab`: local-connection hosts plus one host whose ssh port refuses connections, so changed, failed, skipped and unreachable all happen without containers or a network. Variable precedence is checked against real `ansible-inventory` output.
