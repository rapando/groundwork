#!/usr/bin/env python3
"""Inventory from `terraform output -json ansible_hosts`.

GROUNDWORK_ENV selects the Terraform workspace (default: dev).
"""
import json
import os
import subprocess

ENV = os.environ.get("GROUNDWORK_ENV", "dev")
TF_DIR = os.path.join(os.path.dirname(__file__), "..", "..", "..", "deploy", "terraform")


def main():
    out = subprocess.run(
        ["terraform", f"-chdir={TF_DIR}", "output", "-json", "ansible_hosts"],
        check=True, capture_output=True, text=True, env=dict(os.environ, TF_WORKSPACE=ENV),
    ).stdout
    groups = json.loads(out)
    inv = {"_meta": {"hostvars": {}}, ENV: {"children": list(groups)}}
    for group, hosts in groups.items():
        inv[group] = {"hosts": [h["name"] for h in hosts]}
        for h in hosts:
            inv["_meta"]["hostvars"][h["name"]] = {"ansible_host": h["ip"]}
    print(json.dumps(inv))


main()
