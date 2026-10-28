#!/usr/bin/env python3
"""Ansible inventory script: builds inventory from `terraform output -json`.

Expects an output named `ansible_hosts`: a map of group -> [{name, ip, ...}].
`ip` becomes `ansible_host`; every other field (ansible_user, ansible_port,
ansible_connection, your own variables, ...) becomes a host variable as is,
so a field named ansible_host wins over `ip`. Only `name` is required.
Select the environment with GROUNDWORK_ENV (default: dev).
"""
import json
import os
import subprocess
import sys

ENV = os.environ.get("GROUNDWORK_ENV", "dev")
TF_DIR = os.path.join(os.path.dirname(__file__), "..", "..", "terraform", "envs", ENV)


def main():
    if "--host" in sys.argv:
        print("{}")
        return
    env = os.environ
    out = subprocess.run(
        ["terraform", f"-chdir={TF_DIR}", "output", "-json", "ansible_hosts"],
        check=True, capture_output=True, text=True, env=env,
    ).stdout
    groups = json.loads(out)
    inv = {"_meta": {"hostvars": {}}, ENV: {"children": list(groups)}}
    for group, hosts in groups.items():
        inv[group] = {"hosts": [h["name"] for h in hosts]}
        for h in hosts:
            hv = inv["_meta"]["hostvars"].setdefault(h["name"], {})
            if h.get("ip") is not None:
                hv["ansible_host"] = h["ip"]
            hv.update({k: v for k, v in h.items() if k not in ("name", "ip") and v is not None})
    print(json.dumps(inv))


main()
