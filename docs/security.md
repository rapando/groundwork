# Security model

groundwork runs on your machine with your credentials and can run `terraform apply`. The threats it defends against are other things on the same machine and websites in your browser, not you.

## The server

- **Loopback only.** It listens on `127.0.0.1`. Listening elsewhere needs `--host` plus `--i-understand-remote-access`, and prints a warning: anyone who can reach the port and has the URL can run commands as you.
- **Session token.** Each start generates a random 256-bit token. The URL it prints carries it once (`?t=…`); the server swaps it for an `HttpOnly`, `SameSite=Strict` cookie and redirects to a URL without it. API calls also need the token in an `X-Groundwork-Token` header, which other origins can't read or set, so a forged form or image request fails.
- **DNS rebinding.** Requests whose `Host` isn't `127.0.0.1`, `localhost` or `[::1]` with the right port are refused.
- **CSRF.** State-changing requests need an `Origin` (or `Referer`) from the same host.
- **Headers on every response.** A strict Content-Security-Policy (`script-src 'self'`, `frame-ancestors 'none'`), `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` (the token never leaks in a Referer), `nosniff`, and `Cache-Control: no-store` for the page and the API.
- **Bounded input.** JSON bodies are capped at 1 MiB and file writes at 2 MiB.

## Files and commands

- **Paths.** Every path from a request is resolved against the repository, with symlinks followed, and refused if it lands outside it. `.git/`, `.groundwork/`, `.terraform/` and state files can't be read or written through the API.
- **No shell.** Tools run from an argument list, never through a shell. Values from the UI (host patterns, tags, playbooks, lock IDs) are validated, passed as `--flag=value`, and positional arguments come after `--`, so nothing can turn into an option.
- **Approvals.** An apply runs only the saved plan you reviewed, and only if the code, state and plan file are unchanged and the plan isn't older than `terraform.plan_ttl`. Production-like environments, destructive changes and mutating ad-hoc commands need typed confirmation.

## Secrets

- Run logs are redacted as they're written: values of secret-looking environment variables (`*_TOKEN`, `*_SECRET*`, `*PASSWORD*`, AWS keys, …), `password = …`-style assignments, and known credential formats (AWS access keys, bearer tokens, GitHub/GitLab/Slack tokens). Terraform itself prints sensitive values as `(sensitive value)`.
- Plans show sensitive values masked, including the same values where Terraform didn't mark them.
- Revealing a secret decrypts it for that one response only; it's never stored, logged or cached. An audit list records which secret was revealed and when, never the value.
- groundwork never stores credentials. It uses whatever your shell provides (AWS profiles, SSO, SSH agent, vault password files).

## Network

groundwork makes no network requests of its own except a drift webhook you configure. Everything else (provider downloads, cloud APIs, SSH) is done by terraform, ansible and the cloud CLIs you run.

## Tests

`internal/server/security_test.go` walks every API route and checks it rejects missing credentials, wrong tokens, foreign Origins and rebinding Hosts; that path escapes and argument injection are refused everywhere; and that headers are set on every kind of response. Fuzz tests cover path resolution, log and tool-output parsers, the redactor and the secret scanner (`make fuzz`).

Report a vulnerability privately via GitHub security advisories on the repository.
