# app-with-infra

A small Go service that carries its own infrastructure and deployment, the layout groundwork calls an *embedded* repo.

```
main.go, internal/     the application (a tiny HTTP server, with tests)
Dockerfile             optional container build
deploy/terraform/      infrastructure: one root, a workspace per environment
ops/ansible/           deployment: copies the binary and runs it under systemd
Makefile               build the binary the playbook ships
```

Flow: Terraform declares the hosts and exposes them as the `ansible_hosts` output, Ansible deploys the built binary to them.
Terraform uses only the built-in `terraform_data` resource, so everything validates offline.

Try it: `cd examples/app-with-infra && groundwork` (first run shows the Detect screen).
