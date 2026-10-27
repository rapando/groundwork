# Examples

| Example | What it is |
|---|---|
| [docker-env](docker-env) | Terraform creates three servers as Docker containers; Ansible installs RabbitMQ on one, Redis on another and nginx on the third |

It needs Docker, Terraform and Ansible on your machine, and no cloud account. Copy it out of this repository before running groundwork in it: groundwork manages the enclosing git repository (here, the whole groundwork checkout) and creates `.groundwork/` in it.

```
cp -R examples/docker-env /tmp/docker-env && cd /tmp/docker-env && git init -q && groundwork
```

See its [README](docker-env/README.md) for the walk-through.
