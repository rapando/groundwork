<!-- The PR title must be a Conventional Commit (feat:, fix:, docs:, …).
     PRs are squash-merged: the title becomes the commit on main and the line in the release notes. -->

## What

<!-- What changes for the user, in a sentence or two. -->

## Release note

<!-- The sentence users should read in the changelog. Leave as "none" for docs/test/chore changes. -->

none

## How it was tested

<!-- make test / make e2e / make integration, manual steps, platforms. -->

## Checklist

- [ ] Title is a Conventional Commit and reads well in a changelog
- [ ] `make test` and `make lint` pass
- [ ] `internal/server/dist` rebuilt if `web/` changed
- [ ] Docs updated (`docs/`, `docs/security.md` if the security model changed)
- [ ] Breaking change to a stable surface marked with `!` or a `BREAKING CHANGE:` footer
