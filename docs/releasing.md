# Releasing

Releases are automatic. Version numbers follow [Semantic Versioning](https://semver.org) and are worked out from [Conventional Commit](https://www.conventionalcommits.org) messages on `main`.

## Commit prefixes

| Commit | Release |
|---|---|
| `feat!: …`, `fix(api)!: …`, or a `BREAKING CHANGE: …` footer | major |
| `feat: …` / `feat(scope): …` | minor |
| `fix:`, `perf:`, `revert:` | patch |
| `docs:`, `test:`, `refactor:`, `style:`, `build:`, `ci:`, `chore:` | none on their own |

PRs are squash-merged, so the **PR title** is the commit that counts; the `pr-title` check rejects titles that aren't Conventional Commits. The highest bump since the last tag wins: a `fix:` and a `feat:` together make a minor release. While the version is `0.x`, a breaking change bumps minor instead; from 1.0 on it bumps major.

What counts as breaking (and so needs the `!`) is defined in [compatibility](compatibility.md): a change to the meaning of `groundwork.yaml`, the CLI, a `--json` output, a migration path, a release artefact or a supported platform. The HTTP API is not a promised surface.

## What happens on a push to main

The release is part of the `ci` run, not a separate workflow that waits for it:

1. `ci` starts all its jobs at once: `go` (vet, race tests on Linux and macOS, the coverage gate), `web`, three `e2e` shards, `integration` (terraform and tofu), `lint`, `vuln`, `demo` (Linux and macOS), and `release-version`, which runs `scripts/next-version.sh` on the commit. If nothing since the last `vX.Y.Z` tag warrants a release, the other jobs still run but nothing is published.
2. `release-publish` waits for every other job. When all have passed, it tags the commit (for example `v1.1.0`), and goreleaser builds the archives, `checksums.txt`, an SPDX SBOM per archive, a keyless cosign signature of `checksums.txt` (the Sigstore bundle `checksums.txt.sigstore.json`; the job has `id-token: write` for this), and the GitHub release with a changelog grouped by commit type. Publishes are serialised (one `release` concurrency group). Snapshots (`make release-snapshot`, the `demo` CI job) skip signing and SBOMs.

Both jobs call `.github/workflows/release.yml`, which also runs on its own for *Run workflow* and for a pushed tag.

To preview what the next release would be:

```sh
scripts/next-version.sh
```

## Hand-written release notes

The generated changelog is a list of commit titles. For a release that deserves prose, add `docs/release-notes/<version>.md` (for example `docs/release-notes/1.0.0.md`) before the tag is cut: the release workflow uses it in place of the generated notes when the file exists. Say what the release is for, what changed for the user, how to upgrade, and what is known not to work.

## By hand

- **Force a release or a bigger bump:** Actions → release → *Run workflow* on `main`, choosing `patch`, `minor` or `major` (`auto` uses the commit messages only). The release is at least that bump. This path runs `go test ./...` before publishing, not the full `ci` run, so use it on a commit whose `ci` has passed. If the commit is already tagged, nothing is released. Going from 0.x to `v1.0.0` is done this way, with `major`.
- **Pre-release:** push a tag such as `v1.0.0-rc.1`; it's published as a pre-release, isn't "latest", and is ignored when computing the next version.
- **Any specific version:** `git tag -a v1.2.3 -m v1.2.3 && git push origin v1.2.3`.

## Installing a version

```sh
curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | GROUNDWORK_VERSION=v1.0.0 sh
go install github.com/rapando/groundwork/cmd/groundwork@v1.0.0
```

`groundwork version` prints the installed version in either case.
