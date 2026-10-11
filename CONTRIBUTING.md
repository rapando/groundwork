# Contributing

Thanks for helping. This page is the short version of how changes land; the [docs](docs/) describe the product.

## Set up

You need Go (the version in `go.mod`), Node 22, and for the browser and integration tests an installed Chrome plus `terraform`, `ansible-core` and the linters you want to exercise (`tflint`, `yamllint`, `ansible-lint`).

```sh
make build          # SPA (web/) + binary (bin/groundwork)
make test           # Go + web unit tests
make lint           # golangci-lint
make e2e            # Playwright against the built binary; E2E_ARGS=--shard=1/3 to split
make integration    # with real terraform/ansible/linters on PATH
make fuzz           # each fuzz target for 30s (FUZZTIME=5s to go faster)
```

The SPA in `internal/server/dist` is committed and embedded in the binary. After touching `web/`, run `make web` and commit the rebuilt `dist/`; CI fails if it is out of date.

## Commit messages and PR titles

Releases are cut automatically from [Conventional Commits](https://www.conventionalcommits.org) on `main` (see [docs/releasing.md](docs/releasing.md)). PRs are squash-merged, so **the PR title becomes the commit on `main` and the line in the public changelog**. Write it for someone reading the release notes:

- `feat: warn when the service and CLI versions differ`, not `feat: version stuff`.
- `fix: service stop also stops a service started outside the login unit`, not `fix: stop`.
- `feat!:` or a `BREAKING CHANGE:` footer for anything that changes a stable surface listed in [docs/compatibility.md](docs/compatibility.md).
- `docs:`, `test:`, `refactor:`, `ci:`, `chore:`, `build:`, `style:` do not trigger a release.

The `pr-title` check enforces the prefix; reviewers enforce the wording. The PR template has a *Release note* field: fill it with the sentence you would want users to read, and the title should match.

## Tests

- Go: `go test -race ./...` must pass on macOS and Linux. Keep unit tests hermetic; anything that needs a real tool goes behind the `integration` build tag.
- Web: `cd web && npm test` for unit tests, `npm run typecheck` for `vue-tsc`, `npm run lint` for ESLint and Prettier (`npm run format` fixes TypeScript formatting; Vue templates are kept compact by hand). Browser tests include an axe scan of every screen (`e2e/a11y.spec.ts`): new UI must introduce no serious violations.
- Security: `internal/server/security_test.go` walks every route. A new route is covered automatically, but if it takes a path or a value that reaches a subprocess, add it to the injection cases there.
- Fuzz targets live next to their parsers; add one when you add a parser or validator and register it in the `fuzz` target of the Makefile.

## Review

Small PRs land fastest. Describe what the change does for the user, how you tested it, and anything a reviewer should look at first. If the change touches the security model, say so in the description and update `docs/security.md` in the same PR.
