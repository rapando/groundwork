# docker-env

Three servers on your machine, built by Terraform and configured by Ansible: one runs RabbitMQ, one Redis and one nginx. The "servers" are Docker containers, so it needs no cloud account.

| Server | Runs | From this machine |
|---|---|---|
| `docker-env-dev-rabbitmq` | RabbitMQ with the management plugin | AMQP `127.0.0.1:15672`, management UI http://127.0.0.1:25672 (guest / guest) |
| `docker-env-dev-redis` | Redis | `redis-cli -p 16379` |
| `docker-env-dev-nginx` | nginx | http://127.0.0.1:18080 |

Each host port is 10000 + the container port, so it doesn't clash with anything already running on yours.

## How it fits together

- **Terraform** (`terraform/envs/dev`) builds a server image (Debian with Python, from `terraform/images/server`, the way a cloud provider's base image comes with Python) and starts three containers from it on their own Docker network, one per entry in `servers` in `terraform.tfvars`. It only creates the machines; nothing is installed on them.
- Its **`ansible_hosts` output** is the inventory: one group per server (`rabbitmq`, `redis`, `nginx`) with the container as the host. Containers have no SSH, so each host uses the Docker connection plugin (`ansible_connection: community.docker.docker`). `ansible/inventory/tf_outputs.py` turns the output into an Ansible inventory.
- **Ansible** (`ansible/playbooks/site.yml`) applies one role per group: `rabbitmq`, `redis`, `nginx`. Each installs its package, writes its configuration and starts the service.
- **`groundwork.yaml`** ties the two together: the Ansible inventory for `dev` is the Terraform output, so the Overview shows one `dev` environment with both.

## You need

- Docker running (Docker Desktop, or a daemon reachable through `DOCKER_HOST`)
- Terraform 1.5 or later
- Ansible with the `community.docker` collection. The full `ansible` package includes it; with `ansible-core` only, run `ansible-galaxy collection install -r ansible/requirements.yml`.

## Run it with groundwork

Copy the example out of the groundwork repository first: groundwork manages the enclosing git repository and writes `.groundwork/` there.

```sh
cp -R examples/docker-env /tmp/docker-env && cd /tmp/docker-env && git init -q && groundwork
```

1. On the Overview, press **Plan** on `dev`. The plan creates the network, the image and three containers. Review it and approve.
2. On **Inventory**, the three servers appear once the apply has finished. Press **Run playbook** and pick `site.yml`. The dry run shows each server would install its package (configuring and starting it can't be previewed before the package exists); approve it to run for real.
3. Open http://127.0.0.1:18080, the RabbitMQ management UI, or `redis-cli -p 16379 ping`.

To remove everything, pick **Destroy…** from the **⋯** menu on `dev`, review the destroy plan and approve it.

## Run it without groundwork

```sh
cd terraform/envs/dev && terraform init && terraform apply
cd ../../../ansible && ansible-playbook playbooks/site.yml
```

Running the playbook again changes nothing. `terraform destroy` in `terraform/envs/dev` removes the containers and the network.

## Notes

- The containers have no init system, so each service is started through its init script. For Redis, Ansible looks for the running process, because its init script can't report the status there.
- In a dry run, each role refreshes the package index (that installs nothing), so the dry run can say what it would install.
- RabbitMQ lets the `guest` user connect from outside its container (`loopback_users = none`), and Redis runs without protected mode. That is fine here because every port is published on 127.0.0.1 only. Don't copy those settings to a server that is reachable from a network.
- The network module ignores changes to `ipam_config`: the Docker provider (3.x) reads the network back from Docker 29 without its subnet, and would otherwise recreate the network on every plan.
