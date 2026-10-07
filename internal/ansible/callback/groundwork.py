# groundwork callback plugin: streams structured events as NDJSON to the file
# descriptor named in GROUNDWORK_EVENTS_FD, alongside Ansible's normal output.
# Written by groundwork into .groundwork/ansible/callback/ for each run.
from __future__ import annotations

import json
import os
import time

from ansible.plugins.callback import CallbackBase

DOCUMENTATION = """
name: groundwork
type: notification
short_description: structured events for groundwork
description: Emits play/task/host results as NDJSON for the groundwork UI.
requirements:
  - GROUNDWORK_EVENTS_FD environment variable
"""

MAX = 20000  # characters kept from any one message or diff

# The only facts groundwork keeps. ansible_env and friends are deliberately
# excluded: they routinely contain credentials.
FACT_KEYS = (
    "ansible_distribution", "ansible_distribution_version", "ansible_os_family", "ansible_system",
    "ansible_kernel", "ansible_architecture", "ansible_processor_vcpus", "ansible_memtotal_mb",
    "ansible_python_version", "ansible_hostname", "ansible_fqdn", "ansible_default_ipv4",
)


def _clip(s):
    if s is None:
        return None
    s = str(s)
    return s if len(s) <= MAX else s[:MAX] + "\n… truncated …"


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "notification"
    CALLBACK_NAME = "groundwork"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._out = None
        fd = os.environ.get("GROUNDWORK_EVENTS_FD")
        if fd:
            try:
                self._out = os.fdopen(int(fd), "w", buffering=1)
            except OSError:
                self._out = None
        self._play = ""
        self._task = ""
        self._task_start = {}

    def _emit(self, kind, **data):
        if self._out is None:
            return
        data["type"] = kind
        data["t"] = time.time()
        try:
            self._out.write(json.dumps(data, default=str) + "\n")
        except (OSError, ValueError):
            self._out = None

    # ---- playbook / play / task ----
    def v2_playbook_on_start(self, playbook):
        self._emit("playbook_start", playbook=os.path.basename(getattr(playbook, "_file_name", "") or ""))

    def v2_playbook_on_play_start(self, play):
        self._play = play.get_name().strip()
        self._emit("play_start", play=self._play)

    def v2_playbook_on_task_start(self, task, is_conditional):
        self._task = task.get_name().strip()
        self._task_start[task._uuid] = time.time()
        self._emit("task_start", play=self._play, task=self._task, action=task.action)

    def v2_playbook_on_handler_task_start(self, task):
        self._task = task.get_name().strip()
        self._task_start[task._uuid] = time.time()
        self._emit("task_start", play=self._play, task=self._task, action=task.action, handler=True)

    # ---- per-host results ----
    def _result(self, status, result, **extra):
        r = result._result or {}
        no_log = bool(r.get("_ansible_no_log"))
        msg = None
        if status in ("failed", "unreachable"):
            msg = "(output hidden: no_log)" if no_log else (r.get("msg") or r.get("stderr") or r.get("reason") or "")
        diff = None
        if not no_log and r.get("diff") and status in ("ok", "changed"):
            diff = self._diff_text(r["diff"])
        facts = None
        if not no_log and status == "ok" and isinstance(r.get("ansible_facts"), dict):
            f = r["ansible_facts"]
            facts = {k: f[k] for k in FACT_KEYS if k in f}
            ip = facts.get("ansible_default_ipv4")
            if isinstance(ip, dict):  # keep only the address
                facts["ansible_default_ipv4"] = ip.get("address")
        task = result._task
        started = self._task_start.get(task._uuid)
        self._emit(
            "host_result",
            play=self._play,
            task=task.get_name().strip(),
            action=task.action,
            host=result._host.get_name(),
            status=status,
            changed=bool(r.get("changed")),
            msg=_clip(msg),
            diff=_clip(diff),
            duration=round(time.time() - started, 3) if started else None,
            facts=facts,
            **extra,
        )

    def _diff_text(self, diff):
        try:
            diffs = diff if isinstance(diff, list) else [diff]
            parts = [self._get_diff(d) for d in diffs]
            return "".join(p for p in parts if p)
        except Exception:  # never let rendering a diff break a run
            return None

    def v2_runner_on_ok(self, result):
        self._result("changed" if (result._result or {}).get("changed") else "ok", result)

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self._result("failed", result, ignored=bool(ignore_errors))

    def v2_runner_on_skipped(self, result):
        self._result("skipped", result)

    def v2_runner_on_unreachable(self, result):
        self._result("unreachable", result)

    # ---- totals ----
    def v2_playbook_on_stats(self, stats):
        hosts = {}
        for h in sorted(stats.processed.keys()):
            s = stats.summarize(h)
            hosts[h] = {k: s.get(k, 0) for k in ("ok", "changed", "failures", "unreachable", "skipped", "rescued", "ignored")}
        self._emit("stats", hosts=hosts)
