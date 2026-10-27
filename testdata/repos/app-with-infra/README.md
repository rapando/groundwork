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

This is the repository `scripts/demo.sh` drives end to end (install, init, check, doctor, plan, approve, apply, drift), in CI on Linux and macOS: it needs no Docker and no cloud account. To try it by hand, use a copy outside this repository (inside it, groundwork would manage the whole groundwork checkout):

```
cp -R testdata/repos/app-with-infra /tmp/app-with-infra && cd /tmp/app-with-infra && git init && groundwork
```

The first visit shows the Detect screen.
