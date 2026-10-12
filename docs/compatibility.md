# Compatibility

What 1.0 promises: which surfaces are stable, what a change to each one bumps, and how data written by one release is handled by another. Versions follow [semantic versioning](https://semver.org); see [releasing](releasing.md) for how a version is chosen from commit messages.

## Surfaces

| Surface | Guarantee | A change that breaks it bumps |
|---|---|---|
| `groundwork.yaml` (`version: 1`) | **Stable.** Every key documented in these pages keeps its meaning. New keys may be added in a minor release; a file written by a newer release opens in an older one, with unknown top-level keys ignored and a warning shown. Unknown keys inside a known block are still errors (they are almost always typos). | major (then `version: 2`, see below) |
| CLI commands, flags, exit codes | **Stable.** Commands and flags are only added, never renamed or removed; exit codes (`groundwork check` returns 1 on findings at or above `--fail-on`, 2 on a config error, 3 when a tool could not run) keep their meaning. `--json` outputs may gain fields, never lose or rename them. | major |
| Per-repository `.groundwork/` (`state.db`, `runs/`, `rules/`) | **Forward-migrated.** Any release may migrate it when the project is next opened. Before migrating, `state.db` must pass SQLite's integrity check and is copied to `state.db.bak-<schema>`. A database written by a newer release is refused with a message, never migrated down. `.groundwork/VERSION` records the last release that opened it. | nothing: there is no downgrade path, so keep the backup if you plan to go back |
| Service data directory (`projects.json`, `auth.json`, `token`, `server.json`, `version`, `repos/`, `groundwork.log`) | **Forward-migrated.** `projects.json` and `auth.json` carry `"format": 1`; a higher format is refused. `version` records the release that last ran. | nothing (same as above) |
| HTTP API (`/api/…`) and SSE event names | **Private to the bundled UI.** They change whenever the UI needs them to, in any release. Scripts should use the CLI (`groundwork check --json`, `groundwork doctor --json`, `groundwork projects`); ask for a CLI command if you need something the API has and the CLI doesn't. | nothing |
| `install.sh`, release archive names, `checksums.txt`, signature and SBOM files | **Stable.** Names and the verification procedure in [install](install.md) keep working. | major |
| Supported platforms and tool floors | macOS and Linux on amd64/arm64; terraform ≥ 1.4 or tofu ≥ 1.6; ansible-core ≥ 2.14. Raising a floor is a minor release and is announced in the release notes; Doctor warns below the floor. | minor |

## What "breaking" means

A commit is breaking (and must be `feat!:`/`fix!:` or carry a `BREAKING CHANGE:` footer) when it:

- changes the meaning of, renames or removes a `groundwork.yaml` key, a CLI command, flag or exit code, or a field of a `--json` output;
- removes a migration path, so that a `.groundwork/` or data directory from the previous major release can no longer be opened;
- changes a release artefact name or the way signatures are verified;
- drops a platform.

Adding a key, a command, a flag or a JSON field is not breaking. Changing the HTTP API is not breaking, because it is not a promised surface. Fixing behaviour that contradicted the documentation is not breaking.

## Config versions

`groundwork.yaml` starts with `version: 1` and this release rejects any other value. If a future major release needs `version: 2`, it will:

1. keep reading `version: 1` files unchanged for at least that whole major release;
2. provide `groundwork init --migrate`, which rewrites the file in place and keeps the original as `groundwork.yaml.bak`;
3. never rewrite the file on its own: opening a project does not change `groundwork.yaml`.

## Upgrading and downgrading

Upgrade: install the new binary, then `groundwork service restart`. Every CLI command warns while the service still runs a different version. Project data is migrated when each project is next opened.

Downgrade: stop the service, install the older binary, and for each project restore `state.db.bak-<schema>` over `state.db` if the newer release migrated it (it tells you so in the log). Data the newer release wrote is otherwise refused by the older one with a message naming both versions.

## Defaults that may become configurable

These are built in today and listed so that making them configurable later is an addition, not a change: log head cap (45 MiB) and tail (2,000 lines) per run; check timeouts per tool; the 25-second graceful stop on shutdown; port 7420. Run retention (500 runs, 30 days) and run timeouts (2 hours) are already in `groundwork.yaml` under `retention` and `run_timeout`.
