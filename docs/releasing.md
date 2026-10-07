# Releasing

Releases are automatic. Version numbers follow [Semantic Versioning](https://semver.org) and are worked out from [Conventional Commit](https://www.conventionalcommits.org) messages on `main`.

## Commit prefixes

| Commit | Release |
|---|---|
| `feat!: …`, `fix(api)!: …`, or a `BREAKING CHANGE: …` footer | major (minor while the version is `0.x`) |
| `feat: …` / `feat(scope): …` | minor |
| `fix:`, `perf:`, `revert:` | patch |
| `docs:`, `test:`, `refactor:`, `style:`, `build:`, `ci:`, `chore:` | none on their own |

PRs are squash-merged, so the **PR title** is the commit that counts; the `pr-title` check rejects titles that aren't Conventional Commits. The highest bump since the last tag wins: a `fix:` and a `feat:` together make a minor release.

## What happens on a push to main

1. `ci` runs.
2. When it passes, `release` runs `scripts/next-version.sh` on that commit. If no commit since the last `vX.Y.Z` tag warrants a release, it stops.
3. Otherwise it tags the commit (`v0.4.0`), and goreleaser builds the archives, `checksums.txt`, and the GitHub release with a changelog grouped by commit type.

The first release is `v0.1.0`. To preview what the next one would be:

```sh
scripts/next-version.sh
```

## By hand

- **Force a release or a bigger bump:** Actions → release → *Run workflow* on `main`, choosing `patch`, `minor` or `major`. Going to `v1.0.0` is done this way (`major`), since breaking changes only bump minor before 1.0.
- **Pre-release:** push a tag such as `v1.0.0-rc.1`; it's published as a pre-release, isn't "latest", and is ignored when computing the next version.
- **Any specific version:** `git tag -a v1.2.3 -m v1.2.3 && git push origin v1.2.3`.

## Installing a version

```sh
curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | GROUNDWORK_VERSION=v0.4.0 sh
go install github.com/rapando/groundwork/cmd/groundwork@v0.4.0
```

`groundwork version` prints the installed version in either case.
