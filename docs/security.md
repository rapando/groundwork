# Security model

groundwork runs on your machine with your credentials and can run `terraform apply`. The threats it defends against are other things on the same machine and websites in your browser, not you.

## The server

- **Loopback only.** It listens on `127.0.0.1`. Listening elsewhere needs `--host` plus `--i-understand-remote-access`, and prints a warning: anyone who can reach the port and knows the password can run commands as you.
- **Browser sign-in.** The console lives at `http://localhost:7420` and asks for a password or a passkey (Touch ID, Windows Hello, a phone or a security key, through WebAuthn with user verification required). Nothing is set up on first start: the first visit from this machine sets sign-in up, and setup is refused from any other address, so a `--host 0.0.0.0` service can't be claimed from the network. Passwords are stored as argon2id hashes; passkeys as their public keys. Both live in `auth.json` in the data directory (mode 0600). After five wrong passwords in a row each further attempt waits, from 1 s doubling to a minute. A sign-in sets an `HttpOnly`, `SameSite=Strict` cookie signed with a key in `auth.json`, valid for 7 days. "Sign out other browsers" replaces the key; `groundwork auth reset` deletes the file, which signs everyone out and reopens first-run setup (it's the way back in if you're locked out, and it needs access to your account on this machine). The last sign-in method can't be removed from the console.
- **localhost, not 127.0.0.1.** Passkeys are bound to a host name and the cookie to a host, so page loads on `127.0.0.1` or `[::1]` redirect to `localhost`. Passkeys don't work on a `--host` address (WebAuthn needs a secure context, which plain HTTP only is on localhost); use a password there.
- **CLI token.** The service generates a random 256-bit token on first start and keeps it in its data directory (mode 0600); delete the `token` file and restart to rotate it. The CLI reads it from there and sends it as an `X-Groundwork-Token` header. The signed-in page carries it too, and every API call has to send it in that header, which other origins can't read or set, so a forged form or image request fails even with the cookie. A signed-out page never contains it, and neither do printed URLs, logs or `server.json`.
- **DNS rebinding.** Requests whose `Host` isn't `127.0.0.1`, `localhost` or `[::1]` with the right port are refused.
- **CSRF.** State-changing requests need an `Origin` (or `Referer`) from the same host.
- **Headers on every response.** A strict Content-Security-Policy (`script-src 'self'`, `frame-ancestors 'none'`), `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `nosniff`, and `Cache-Control: no-store` for the page and the API.
- **Bounded input.** JSON bodies are capped at 1 MiB and file writes at 2 MiB.
- **What's deliberately not there.** First-run setup goes to whoever reaches the console first from this machine; on a shared machine, set sign-in up as soon as you install. The server sets `ReadHeaderTimeout` (10s) and `IdleTimeout` (2min) but no read or write deadline, because event streams and large saves are long-lived by design; a local client that holds a connection open only costs itself.

- **One service, many projects.** Every project's API sits behind the same checks; a project is only reachable once you've imported it. Importing a folder refuses `/` and your home directory. Cloning accepts only `https://`, `ssh://` and `git@host:path` URLs, runs `git` with `protocol.ext.allow=never` and with prompts disabled, and passes the URL after `--`.

## Files and commands

- **Paths.** Every path from a request is resolved against the repository, with symlinks followed, and refused if it lands outside it. `.git/`, `.groundwork/`, `.terraform/` and state files can't be read or written through the API.
- **No shell.** Tools run from an argument list, never through a shell. Values from the UI (host patterns, tags, playbooks, lock IDs) are validated, passed as `--flag=value`, and positional arguments come after `--`, so nothing can turn into an option.
- **`groundwork.yaml` is repository-controlled**, including in repositories cloned through the UI, so it's validated like request input: every path must stay inside the repository (`vault_password_file` may also be `~/…` or absolute, never `..`), environment and workspace names are `[A-Za-z0-9_.-]`, the drift webhook must be an `http(s)` URL with a host and no credentials, and an executable vault password file (which ansible would run) is refused unless `ansible.allow_vault_script: true`.
- **Approvals.** An apply runs only the saved plan you reviewed, and only if the code, state and plan file are unchanged and the plan isn't older than `terraform.plan_ttl`. Production-like environments, destructive changes and mutating ad-hoc commands need typed confirmation.

## Secrets

- Run logs are redacted as they're written: values of secret-looking environment variables (`*_TOKEN`, `*_SECRET*`, `*PASSWORD*`, AWS keys, …), the values a run gives to variables declared `sensitive = true` (from tfvars, `-var-file` files, defaults and `TF_VAR_*`), `password = …`-style assignments, and known credential formats (AWS access keys, bearer tokens, GitHub/GitLab/Slack tokens). Terraform itself prints sensitive values as `(sensitive value)`, but a provider or a stage error may echo one, so the same masking applies to stage error details and the run's error summary.
- The same masking covers everything else that is stored or shown from a tool's output: linter and `terraform validate` findings in the Problems tray, and `git diff` in the Code view. Values revealed from SOPS or vault files are returned once for that response and are never stored, so there is nothing to redact later.
- Plans show sensitive values masked, including the same values where Terraform didn't mark them.
- Revealing a secret decrypts it for that one response only; it's never stored, logged or cached. An audit list records which secret was revealed and when, never the value.
- groundwork never stores credentials. It uses whatever your shell provides (AWS profiles, SSO, SSH agent, vault password files).

## Network

groundwork makes no network requests of its own except a drift webhook you configure. Everything else (provider downloads, cloud APIs, SSH) is done by terraform, ansible and the cloud CLIs you run.

## Tests

`internal/server/security_test.go` walks every API route and checks it rejects missing credentials, wrong tokens, foreign Origins and rebinding Hosts; that path escapes and argument injection are refused everywhere; and that headers are set on every kind of response. Fuzz tests cover path resolution, log and tool-output parsers, the redactor and the secret scanner (`make fuzz`).

Report a vulnerability privately via GitHub security advisories on the repository.
