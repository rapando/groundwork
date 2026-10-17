# ansible-only

A pure Ansible repo: one playbook, one role (`nginx`), static inventories for `dev` and `prod`.

```
ansible.cfg
inventory/dev.yml, prod.yml    hosts per environment
group_vars/                    per-environment variables
playbooks/site.yml
roles/nginx/                   install, configure, serve
```

Try it on a copy outside this repository (inside it, groundwork would manage the whole groundwork checkout):

```
cp -R examples/ansible-only /tmp/ansible-only && cd /tmp/ansible-only && git init && groundwork
```

A dry run from a terminal: `ansible-playbook playbooks/site.yml --check --diff -l dev`.
