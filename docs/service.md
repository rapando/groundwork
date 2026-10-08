# The service and projects

groundwork runs as one background service on your machine, like a database or a message broker. It hosts all your infrastructure repositories as **projects**, each with its own checks, runs, file watching and drift schedule, behind one console at `http://127.0.0.1:7420`.

![Projects](images/projects.png)

## Starting it

```sh
cd ~/code/platform-infra
groundwork
```

If the service isn't running, this starts it in the background. It then imports the repository you're in as a project and opens it in your browser. Running `groundwork` in another repository adds that one too. The terminal is free as soon as the browser opens.

To have the service start when you log in:

```sh
groundwork service install
```

This registers a launchd agent on macOS (`~/Library/LaunchAgents/io.github.rapando.groundwork.plist`) or a systemd user unit on Linux (`~/.config/systemd/user/groundwork.service`), then starts it. The service restarts if it crashes.

> **PATH.** A login service doesn't get your shell's `PATH`, so `install` records the `PATH` of the shell you run it from. That is how the service finds terraform, ansible, sops and the linters. If you install or move tools later, run `groundwork service install` again.

| Command | |
|---|---|
| `groundwork service status` | running or stopped, project count, data directory, log file, console URL |
| `groundwork service start` | start it (through the login service if installed, else in the background) |
| `groundwork service stop` | stop it; runs in progress are stopped gracefully (terraform gets time to release its state lock) |
| `groundwork service install` / `uninstall` | add or remove the login service |
| `groundwork serve [folder…]` | run it in the foreground instead, e.g. in a container or to watch its log; folders given are imported |

Only one service runs per data directory. A second `serve` notices the running one, imports any folders you gave it there, and exits.

## Projects

Import a project from the console's **Projects** page (the groundwork logo or **All projects** in the sidebar), or from a terminal:

```sh
groundwork add ~/code/platform-infra             # a folder on this machine, used in place
groundwork add git@github.com:acme/edge.git      # a repository URL, cloned first
groundwork projects                              # list, with status
groundwork open platform-infra                   # open one in the browser
groundwork remove platform-infra                 # stop managing it
```

- **A folder** is used where it is. If it's inside a git repository, the repository root is imported, the same root groundwork has always used. Importing the same repository twice returns the existing project.
- **A URL** (`https://…`, `ssh://…` or `git@host:path`) is cloned with your own `git`, so your SSH keys and credential helpers apply, into `repos/` in the data directory. The service has no terminal, so git can't prompt: use an SSH key loaded in your agent, or a credential helper, for private repositories.
- **Removing** a project stops the service watching it. The files, including its `.groundwork/` run history, stay where they are, so importing it again picks up where you left off. A project with a run in progress can't be removed.
- If a project's folder disappears (an unmounted disk, a deleted checkout), the service keeps running and marks that project **unavailable**. Use **Retry** on the Projects page once it's back.

`remove` and `open` accept a project id, its name, or a path to it. An unknown name is an error and never falls back to the repository you're standing in.

Each project lives at `/p/<id>/` in the console, so links to a run or a file can be bookmarked and shared between your own tabs.

## Where things are kept

| What | Where |
|---|---|
| Project list, session token, `server.json`, `groundwork.log` | the data directory: `~/Library/Application Support/groundwork` (macOS), `~/.config/groundwork` (Linux), or `$GROUNDWORK_HOME` |
| Cloned repositories | `repos/` in the data directory |
| A project's runs, plans, check caches, custom rules | `.groundwork/` in that repository (git-ignored), as before |

The session token persists across restarts, so open tabs keep working when the service restarts at login. `groundwork service status` prints a URL that logs a browser in.

The CLI commands that work on a single repository (`init`, `check`, `doctor`, `drift`) don't need the service and run directly in the repository, so they still fit CI.

## When something's wrong

- **The console doesn't load:** `groundwork service status`, then read the log it names.
- **"terraform not found" only in the console:** the service's `PATH` is out of date. Re-run `groundwork service install` from a shell where `terraform` works.
- **Port 7420 is taken:** the service picks a free port; `groundwork service status` shows which. To pin one, run `groundwork service start --port <n>` when no login service is installed.
- **Stuck after an upgrade:** the running service is still the old binary. Run `groundwork service stop`, then `groundwork service start`.
