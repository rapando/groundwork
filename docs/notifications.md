# Notifications

groundwork tells you when a run needs you or is over: a run failed, a plan or dry run is waiting for approval, a long run succeeded, or drift turned up. Each notification is kept in the project's list under the bell, and can also show a toast in the console, a desktop notification, and a POST to a webhook.

## What notifies

| Event | When | Title, for example |
|---|---|---|
| `failed` | a run fails (including one stopped by its `run_timeout`) | *Apply #14 failed on prod* |
| `approval` | a Terraform plan with changes, or an Ansible dry run, waits for approval | *Plan #15 is ready for review* |
| `succeeded` | a run succeeds | *Playbook site.yml succeeded on dev* |
| `cancelled` | a run is cancelled or a waiting plan is discarded | *Apply #16 was cancelled on prod* |
| `drift` | a drift check (scheduled or from the Graph) finds drift | *Drift on prod: 2 resources* |

The line under the title says where and how long: *terraform/envs/prod · 2m 41s · State locked on terraform/envs/prod (prod)*. For a failure that [Troubleshoot](troubleshooting.md) matched to a known cause, the notification names the cause and links its first fix; otherwise it shows the error's first line. A plan waiting for approval shows what it adds, changes, replaces and destroys, and how many dangerous changes it has. Approving always happens on the plan page, so danger acknowledgements and typed confirmations can't be skipped from a notification.

Every event is recorded in the list, whatever the settings below say, once per run and event. The list keeps 30 days. Opening a run marks its notifications read; **Mark all read** clears the rest.

## Defaults

With no settings saved for all projects and no `notify:` block in `groundwork.yaml`:

| Event | Toast | Desktop | Webhook |
|---|---|---|---|
| Run failed | yes | yes | if a webhook is set |
| Waiting for approval | yes | yes | no |
| Run succeeded | only runs of 30s or longer | no | no |
| Run cancelled | no (recorded only) | no | no |
| Drift found | yes | as `drift.notify` says (desktop by default) | as `drift.notify` says |

## Configuration

Settings come in two layers, both on the console's **Notifications** page:

- **All projects**: kept by the service in `notify.yaml` in its [data directory](service.md#where-things-are-kept), not in any repository. Every project starts from them. The project list reaches them from the bell (**Notification settings →**).
- **This project**: overrides in the project's `groundwork.yaml`. Change a row or a field and it becomes an override, marked *This project* with a **Reset**; everything you leave alone follows All projects, so a later change there reaches the project at once. Only the overrides are written (the rest of the file, comments included, stays as it was), and resetting the last one removes the block.

Each key comes from the first of: the project's `notify:` block, its older `drift.notify`/`drift.webhook`, All projects, the default. Keep a personal webhook in All projects so a shared repository doesn't carry it.

Both files use the same block:

```yaml
notify:
  failed: [console, desktop, webhook]
  approval: [console, desktop]
  success: [console]
  drift: [console, desktop, webhook]
  quiet_under: 30s          # successes shorter than this: recorded, nothing else
  envs: [prod]              # optional: desktop and webhook only for these environments
  webhook: https://hooks.example.com/…
```

- Each event lists its channels: `console` (a toast in the console), `desktop` and `webhook`. A list you leave out follows the layer below; an empty list (`[]`) means record only.
- `quiet_under` is a duration (`30s`, `2m`); it applies to successes only.
- `envs` limits desktop notifications and the webhook to those environments; the console still shows the rest. Empty means every environment (in a project, `envs: []` lifts a limit set for all projects).
- `webhook` must be an `http://` or `https://` URL with a host and no credentials. The page refuses a `webhook` channel with no URL in either layer (or `drift.webhook`); in a hand-edited `groundwork.yaml` such a channel just sends nothing until a URL is set.
- The older `drift.notify` and `drift.webhook` keep working: with no `drift` list in the project's `notify:` block, `drift.notify: desktop` means `drift: [console, desktop]`, `webhook` means `drift: [console, webhook]`, `none` means `drift: [console]`; and `drift.webhook` is the webhook unless the block sets one.

Edits to either file, by the page or by hand, apply without a restart. A broken `notify.yaml` is reported on the page and in the service log, and the defaults apply until it is fixed. **Send a test** uses the saved settings: a desktop notification from the service if any event lists `desktop`, and a test POST if any event lists `webhook` and a URL is set. It reports what was sent and what failed.

## For one run

While a run is going, **When it finishes ▾** on its page overrides the settings for that run only:

| Mode | |
|---|---|
| **Project default** | the project's settings |
| **When it finishes** | a toast and a desktop notification whatever happens (failed, waiting for approval, or succeeded, however short); the webhook as the settings say |
| **Only if it fails** | a failure notifies as the settings say; anything else is recorded only |
| **Don't notify** | recorded only |

## Desktop notifications

While a console tab is open, the browser shows desktop notifications (once you allow them), and clicking one opens the run. It skips the run you are looking at. When no console is open, the service shows them itself, with macOS Notification Center (`osascript`) or `notify-send` on Linux; those can't open the run on click. The text is passed as arguments, never as a script.

Drift from the [schedule](troubleshooting.md#scheduled-drift) reaches the desktop and the webhook once per change: unchanged drift isn't sent again, and drift that goes away and comes back is. A drift check you start yourself is shown in the console.

## Webhook payload

A JSON `POST` with `Content-Type: application/json`, a 10-second timeout and no redirects followed. `text` is ready for Slack, Teams or Mattermost incoming webhooks:

```json
{
  "text": "infra: Apply #14 failed on prod (terraform/envs/prod · 2m 41s · State locked on terraform/envs/prod (prod))",
  "event": "failed",
  "title": "Apply #14 failed on prod",
  "project": "infra",
  "root": "terraform/envs/prod",
  "env": "prod",
  "run_id": 14,
  "kind": "tf.plan",
  "duration_s": 161,
  "cause": { "issue_id": 3, "rule_id": "tf-state-lock", "title": "State locked on terraform/envs/prod (prod)", "action": "Release the lock" }
}
```

`event` is `failed`, `approval` or `succeeded` (`test` for a test send); `cause` is only present for a failure with a known cause. Scheduled drift sends `text`, `event: "drift"`, `project`, `kind`, `root`, `env`, `run_id`, `drift_count` and `addresses`. Fields may be added in later releases, never removed or renamed.

## Across projects

The bell in the console's header counts unread notifications across every open project, and the project list shows each project's unread count, unread failures and plans waiting for approval. Notifications are stored per project, in its `.groundwork/state.db`.
