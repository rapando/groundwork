# ansible-only

A pure Ansible repo: one playbook, one role (`nginx`), static inventories for `dev` and `prod`.

```
ansible.cfg
inventory/dev.yml, prod.yml    hosts per environment
group_vars/                    per-environment variables
playbooks/site.yml
roles/nginx/                   install, configure, serve
```

Try it: `cd examples/ansible-only && groundwork`
(run a dry run with `ansible-playbook playbooks/site.yml --check --diff -l dev`)
