# The service and projects

groundwork runs as one background service on your machine, like a database or a message broker. It hosts all your infrastructure repositories as **projects**, each with its own checks, runs, file watching and drift schedule, behind one console at `http://localhost:7420`.

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
| `groundwork service status` | running or stopped; version, uptime, port, projects available/unavailable, running jobs, data directory, log file, console URL |
| `groundwork service start` | start it (through the login service if installed, else in the background) |
| `groundwork service stop` | stop it; runs in progress are stopped gracefully (terraform gets time to release its state lock) |
| `groundwork service restart` | stop and start again, e.g. after installing a new version |
| `groundwork service logs [-f] [-n N]` | print the last N lines of the service log, and follow it with `-f` |
| `groundwork service install` / `uninstall` | add or remove the login service |
| `groundwork serve [folder…]` | run it in the foreground instead, e.g. in a container or to watch its log; folders given are imported |

**Stopping.** `groundwork service stop`, the login service stopping at logout, or SIGTERM/SIGINT give every run in progress up to 25 seconds: each tool gets SIGINT (terraform releases its state lock and exits; ansible stops after the current task), then SIGKILL, and the run is marked cancelled. launchd and systemd wait 40 seconds before killing the service themselves (`ExitTimeOut` / `TimeoutStopSec`). A *hard* stop (`kill -9`, power loss) can't do any of that: the tools keep running on their own. Each run records its tool's process group, so on the next start groundwork raises an issue for any that is still alive, with the commands to inspect or stop it, and marks the interrupted runs failed. Every run also has a `run_timeout` (default 2 hours, in `groundwork.yaml`) after which it is stopped the same way and fails, so a hung provider or host cannot hold a root's lock forever.

Only one service runs per data directory: `serve` holds a lock on `<data dir>/lock` for its lifetime (released by the kernel if it crashes). A second `serve`, started by hand or by the login service, sees the lock, imports any folders you gave it into the running one, and exits.

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
| Project list, `auth.json` (sign-in), `token` (the CLI's), `server.json`, `groundwork.log` | the data directory: `~/Library/Application Support/groundwork` (macOS), `~/.config/groundwork` (Linux), or `$GROUNDWORK_HOME` |

The service writes its own log, `groundwork.log` in the data directory, rotated at 10 MiB with three old files kept (`groundwork.log.1` to `.3`), so it stays bounded however long the machine runs. It never contains the CLI token. `GROUNDWORK_LOG=debug` (or `--log-level debug` on `serve`) adds a line per HTTP request; `GROUNDWORK_LOG_FORMAT=json` (`--log-format json`) makes each line a JSON object for log shippers. When `serve` runs in a terminal the same lines also go to stderr.
| Cloned repositories | `repos/` in the data directory |
| A project's runs, plans, check caches, custom rules | `.groundwork/` in that repository (git-ignored), as before |

Sign-ins persist across restarts, so open tabs keep working when the service restarts at login. The console asks for a password or passkey; the first visit from this machine sets them up, and the **Sign-in** button on the project list changes them. `groundwork auth status` shows what is set up, and `groundwork auth reset` removes it all if you are locked out.

The CLI commands that work on a single repository (`init`, `check`, `doctor`, `drift`) don't need the service and run directly in the repository, so they still fit CI.

## When something's wrong

- **The console doesn't load:** `groundwork service status`, then read the log it names.
- **"terraform not found" only in the console:** the service's `PATH` is out of date. Re-run `groundwork service install` from a shell where `terraform` works.
- **Port 7420 is taken:** the service refuses to start and says by what (another groundwork, with its pid, or another program), because bookmarks and scripts rely on the port. Pass `--port <n>` to use another, or `--port 0` to take any free one; `groundwork service status` shows the port in use.
- **Stuck after an upgrade:** the running service is still the old binary. Run `groundwork service restart` (every command warns while the versions differ).
- **"groundwork hit an internal error" in the Problems tray:** a background task of that project (a run, a check, the file watcher, a drift check) crashed. groundwork recovers from such crashes rather than exiting, so the other projects and the console are unaffected; the run involved is marked failed with the reason, the watcher or engine loop is restarted, and the stack trace is in the service log. Please report it with that trace.
