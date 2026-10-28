# Tutorial: configure your Terraform hosts with Ansible

You've built servers with Terraform in groundwork. This tutorial configures them with Ansible: it updates every server, installs nginx on two web servers, and installs MySQL, Redis and RabbitMQ on a storage server. Each step says whether it happens **in groundwork** or **in a terminal**.

The servers here are three Ubuntu 24.04 Docker containers, so you can follow along without a cloud account. With real VMs, the Ansible part is the same. Only the connection settings in step 1 change. The [docker-env example](../examples/docker-env) is a complete project built the same way.

## Starting point

A repository set up with groundwork for Terraform and Ansible ([setup](setup.md)), with an applied `dev` environment in `terraform/envs/dev` that has:

- three `docker_container` resources, `storage1`, `backend1` and `frontend1`, created with `for_each` over `var.servers`;
- three `random_password` resources: `mysql_password`, `redis_password` and `rabbitmq_password`;
- the `ansible_hosts` output and `ansible/inventory/tf_outputs.py` that setup wrote.

The result:

| Server | Ansible group | Runs | From your machine |
|---|---|---|---|
| `frontend1` | `frontend` | nginx, page reading "Frontend" | http://127.0.0.1:8081 |
| `backend1` | `backend` | nginx, page reading "Backend" | http://127.0.0.1:8082 |
| `storage1` | `storage` | MySQL, Redis, RabbitMQ | inside Docker only |

## Where each step happens

| Step | In groundwork | In a terminal |
|---|---|---|
| 1. Terraform changes | edit in **Code**, then **Plan** and approve | |
| 2. Ansible collections | | `ansible-galaxy collection install` |
| 3. Check the connection | **Inventory**: **Ping all**, **Gather facts** | |
| 4. Roles and playbook | create files in **Code** | |
| 5. Run the playbook | **Inventory**: **Run playbook** | |
| 6. Check the services | **Inventory**: **Ad-hoc command…** | `curl`, or a browser |

groundwork never installs Ansible collections (it never runs a package manager), so step 2 is the only one that needs a terminal.

## 1. Make the servers reachable by Ansible

**In groundwork.** Containers have no SSH server, and their names aren't in DNS. If Ansible tries to reach them as usual, a ping fails with `Could not resolve hostname storage1`. The Terraform needs four changes:

- **Python in the image.** Ansible modules run Python on the server, and `ubuntu:24.04` has none. You could install it with Ansible's `raw` module, but `raw` doesn't run in a dry run, so every dry run on a new server would fail. A cloud provider's base image already includes Python. Here you build an image that does.
- **The Docker connection.** Set `ansible_connection: community.docker.docker` on every host, so Ansible uses `docker exec` instead of SSH.
- **Groups named by role.** If a group has the same name as a host (`storage1` in `storage1`), Ansible warns `Found both group and host with same name`. Named by role, the groups also let you add `frontend2` later without changing the playbook.
- **Published ports** for the two nginx servers, bound to `127.0.0.1` only.

Create `terraform/images/server/Dockerfile` (**Code** → **+**):

```dockerfile
# Ubuntu plus what Ansible needs to manage it: Python, the apt bindings and ps.
FROM ubuntu:24.04
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 python3-apt procps \
 && rm -rf /var/lib/apt/lists/*
```

In `terraform/envs/dev/main.tf`, replace the image and container resources and add to `locals`:

```hcl
locals {
  name_prefix = "terraform-ansible-${var.environment}"

  # Every host is a container: Ansible reaches it with `docker exec`, not SSH.
  ansible_common = {
    ansible_connection = "community.docker.docker"
    env_name           = var.environment # `environment` is reserved in Ansible
  }

  # Host port each web server's nginx is published on.
  web_ports = {
    frontend1 = 8081
    backend1  = 8082
  }
}

resource "docker_image" "server" {
  name = "${local.name_prefix}-server:24.04"

  build {
    context = "${path.module}/../../images/server"
  }

  triggers = {
    dockerfile = filesha1("${path.module}/../../images/server/Dockerfile")
  }
}

resource "docker_container" "server" {
  for_each = toset(var.servers)

  name    = each.value
  image   = docker_image.server.image_id
  command = ["sleep", "infinity"]

  dynamic "ports" {
    for_each = contains(keys(local.web_ports), each.value) ? [local.web_ports[each.value]] : []
    content {
      internal = 80
      external = ports.value
      ip       = "127.0.0.1"
    }
  }
}
```

Replace the `ansible_hosts` output in `outputs.tf`. Each host's `name` refers to its container, so the output waits for the container to exist. The passwords are passed as host variables ([Passing a generated password to Ansible](setup.md#passing-a-generated-password-to-ansible)). To keep them in ansible-vault instead, see [Saving generated passwords in ansible-vault](setup.md#saving-generated-passwords-in-ansible-vault).

```hcl
output "ansible_hosts" {
  description = "Hosts for Ansible, by group."
  sensitive   = true
  value = {
    storage = [merge(local.ansible_common, {
      name           = docker_container.server["storage1"].name
      mysql_password = random_password.mysql_password.result
      redis_password = random_password.redis_password.result
      rmq_password   = random_password.rabbitmq_password.result
    })]

    backend = [merge(local.ansible_common, {
      name       = docker_container.server["backend1"].name
      page_title = "Backend"
    })]

    frontend = [merge(local.ansible_common, {
      name       = docker_container.server["frontend1"].name
      page_title = "Frontend"
    })]
  }
}
```

In `groundwork.yaml`, point the `dev` inventory at the script. This pairs it with the `dev` Terraform environment on the Overview and in the graph:

```yaml
ansible:
  projects:
    - path: ansible
      config: ansible.cfg
      inventories:
        dev: inventory/tf_outputs.py
```

On the **Overview**, press **Plan** on `dev`. The plan builds the image and **replaces all three containers**, because their image changed. Nothing is installed on them yet, so nothing is lost. Review the plan and approve it.

## 2. Install the Ansible collections

**In a terminal**, from the repository root. Create `ansible/requirements.yml` (in **Code** or your editor):

```yaml
---
collections:
  - name: community.docker    # the Docker connection plugin
  - name: community.mysql     # mysql_db, mysql_user
  - name: community.rabbitmq  # rabbitmq_user
```

```sh
ansible-galaxy collection install -r ansible/requirements.yml
```

If you installed the full `ansible` package rather than `ansible-core`, it already includes all three, and you can skip this.

## 3. Check the connection

**In groundwork.** Open **Inventory** and pick `dev`. The three servers appear under `storage`, `backend` and `frontend`. Press **Ping all**: each server should report reachable. Then press **Gather facts**: each server's OS should show as Ubuntu 24.04.

If the ping says `Could not resolve hostname`, the hosts don't have `ansible_connection` yet. Check that the apply finished, then look at a host's variables on the Inventory screen. If the error names the connection plugin, go back to step 2.

## 4. Write the roles and the playbook

**In groundwork**, on the **Code** screen. **+** creates a file and any folders it needs. groundwork runs `yamllint`, `ansible-lint` and `syntax-check` on each save, so problems show up as you write.

```
ansible/
├── playbooks/site.yml
└── roles/
    ├── common/tasks/main.yml
    ├── nginx/{tasks/main.yml, templates/index.html.j2}
    ├── mysql/{tasks/main.yml, handlers/main.yml}
    ├── redis/{tasks/main.yml, handlers/main.yml}
    └── rabbitmq/tasks/main.yml
```

The containers run `sleep infinity` instead of an init system, so systemd isn't running. The roles use `ansible.builtin.service`, which falls back to each package's `/etc/init.d` script.

A playbook run in groundwork starts with a dry run (`--check --diff`). On a new server the packages aren't installed yet, so configuring and starting them can't be previewed. Each role installs its package first, then skips the rest during a dry run if the install would be a change. The dry run then shows what it would install, and the real run does everything.

`ansible/playbooks/site.yml`:

```yaml
---
- name: Update all servers
  hosts: all
  roles:
    - common

- name: Web servers
  hosts: frontend:backend
  roles:
    - nginx

- name: Storage server
  hosts: storage
  roles:
    - mysql
    - redis
    - rabbitmq
```

`ansible/roles/common/tasks/main.yml`: this updates every server. Running the playbook again later updates them again.

```yaml
---
# Refreshing the package index changes nothing installed, so it runs in a
# dry run too: without it, the dry run can't tell what it would upgrade.
- name: Update the package index
  ansible.builtin.apt:
    update_cache: true
    cache_valid_time: 3600
  check_mode: false
  changed_when: false

- name: Upgrade every package
  ansible.builtin.apt:
    upgrade: dist
```

`ansible/roles/nginx/tasks/main.yml`: nginx reads the page from disk on every request, so changing it needs no reload.

```yaml
---
- name: Install nginx
  ansible.builtin.apt:
    name: nginx
    state: present
  register: nginx_install

- name: Configure and start nginx
  when: not (ansible_check_mode and nginx_install is changed)
  block:
    - name: Write the home page
      ansible.builtin.template:
        src: index.html.j2
        dest: /var/www/html/index.html
        mode: "0644"

    - name: Start nginx
      ansible.builtin.service:
        name: nginx
        state: started
```

`ansible/roles/nginx/templates/index.html.j2`: `page_title` comes from the Terraform output.

```html
<!doctype html>
<html lang="en">
  <head><meta charset="utf-8"><title>{{ page_title }}</title></head>
  <body>
    <h1>{{ page_title }}</h1>
    <p>Served by {{ inventory_hostname }} ({{ env_name }}).</p>
  </body>
</html>
```

`ansible/roles/mysql/tasks/main.yml`: on Ubuntu, MySQL's `root` logs in through the Unix socket. Ansible runs as root in the container, so it needs no password. The role creates an `app` database, and an `app` user with the generated password.

```yaml
---
- name: Install MySQL and the Python driver the mysql modules need
  ansible.builtin.apt:
    name:
      - mysql-server
      - python3-pymysql
    state: present
  register: mysql_install

- name: Configure and start MySQL
  when: not (ansible_check_mode and mysql_install is changed)
  block:
    - name: Accept connections from other containers
      ansible.builtin.lineinfile:
        path: /etc/mysql/mysql.conf.d/mysqld.cnf
        regexp: "^bind-address"
        line: "bind-address = 0.0.0.0"
      notify: Restart mysql

    - name: Start MySQL
      ansible.builtin.service:
        name: mysql
        state: started

    - name: Create the app database
      community.mysql.mysql_db:
        name: app
        state: present
        login_unix_socket: /var/run/mysqld/mysqld.sock

    - name: Create the app user
      community.mysql.mysql_user:
        name: app
        host: "%"
        password: "{{ mysql_password }}"
        update_password: on_create
        priv: "app.*:ALL"
        state: present
        login_unix_socket: /var/run/mysqld/mysqld.sock
      no_log: true
```

`ansible/roles/mysql/handlers/main.yml`:

```yaml
---
- name: Restart mysql
  ansible.builtin.service:
    name: mysql
    state: restarted
```

`ansible/roles/redis/tasks/main.yml`: Ubuntu's `redis.conf` ships with `requirepass` commented out, and the regexp replaces that line. Without systemd, Redis's init script can't report whether it's running, so `pattern` tells Ansible to look for the process instead.

```yaml
---
- name: Install Redis
  ansible.builtin.apt:
    name: redis-server
    state: present
  register: redis_install

- name: Configure and start Redis
  when: not (ansible_check_mode and redis_install is changed)
  block:
    - name: Require a password
      ansible.builtin.lineinfile:
        path: /etc/redis/redis.conf
        regexp: '^#?\s*requirepass\s'
        line: "requirepass {{ redis_password }}"
      no_log: true
      notify: Restart redis

    - name: Start Redis
      ansible.builtin.service:
        name: redis-server
        state: started
        pattern: redis-server
```

`ansible/roles/redis/handlers/main.yml`:

```yaml
---
- name: Restart redis
  ansible.builtin.service:
    name: redis-server
    state: restarted
    pattern: redis-server
```

`ansible/roles/rabbitmq/tasks/main.yml`: this creates an `app` user with the generated password, and removes `guest`.

```yaml
---
- name: Install RabbitMQ
  ansible.builtin.apt:
    name: rabbitmq-server
    state: present
  register: rabbitmq_install

- name: Configure and start RabbitMQ
  when: not (ansible_check_mode and rabbitmq_install is changed)
  block:
    - name: Start RabbitMQ
      ansible.builtin.service:
        name: rabbitmq-server
        state: started

    # Read-only, so it runs in a dry run too.
    - name: Wait for RabbitMQ to finish booting
      ansible.builtin.command: rabbitmqctl await_startup
      check_mode: false
      changed_when: false

    - name: Create the app user
      community.rabbitmq.rabbitmq_user:
        user: app
        password: "{{ rmq_password }}"
        vhost: /
        configure_priv: ".*"
        read_priv: ".*"
        write_priv: ".*"
        state: present
      no_log: true

    - name: Remove the default guest user
      community.rabbitmq.rabbitmq_user:
        user: guest
        state: absent
```

Use `no_log: true` on every task that handles a password. groundwork masks known secret formats in run logs, but a value Terraform generated isn't one of them.

## 5. Run the playbook

**In groundwork.** On **Inventory** (`dev`), press **Run playbook**:

1. Pick `playbooks/site.yml` and **Dry run, then run after approval**, then press **Start**.
2. The run page shows a grid of tasks by host. On new servers, the dry run shows the upgrade and each package install as *changed*, and skips the rest.
3. Approve the dry run. The playbook runs for real, and every cell should end up ok or changed.
4. Run it once more. Nothing should change (`changed=0` on every host): every task describes a state, not an action.

To update all servers later, run the same playbook again. With the servers ticked in the list, **Run playbook on N hosts** limits the run to those hosts.

Nothing restarts the services when a container restarts. After `docker restart` or a reboot, run the playbook again: the start tasks bring everything back.

## 6. Check the services

**In groundwork**, with **Ad-hoc command…** on **Inventory**. `command` can change a server, so groundwork asks you to type `dev` before it runs. These checks don't put a password on the command line, so no password can appear in the run log:

| Hosts | Module | Arguments | Expect |
|---|---|---|---|
| `storage` | `command` | `mysql -e "SELECT user, host FROM mysql.user WHERE user='app'"` | `app` with host `%` |
| `storage` | `command` | `redis-cli ping` | `NOAUTH Authentication required.`: Redis wants the password |
| `storage` | `command` | `rabbitmqctl list_users` | `app`, and no `guest` |

**In a terminal or a browser**, open http://127.0.0.1:8081 and http://127.0.0.1:8082. They should show "Frontend" and "Backend".

## Without groundwork

The same steps from a terminal, in the repository root:

```sh
terraform -chdir=terraform/envs/dev apply
ansible-galaxy collection install -r ansible/requirements.yml
cd ansible
ansible all -m ping
ansible-playbook playbooks/site.yml --check --diff   # the dry run
ansible-playbook playbooks/site.yml
```

## Troubleshooting

| Message | Cause and fix |
|---|---|
| `Could not resolve hostname storage1` | The host has no `ansible_connection`. Apply the output change from step 1. The inventory reads the current state, so it only sees the change after an apply. |
| `the connection plugin 'community.docker.docker' was not found` | The collection isn't installed. See step 2. |
| `/usr/bin/python3: not found` | The container still runs the old image. Check that the plan replaced the containers. |
| `Found both group and host with same name` | The groups are still named after the hosts. Rename them as in step 1. |
| A start task fails with `Could not find the requested service` | That package installed no init script. Check with an ad-hoc `command` of `ls /etc/init.d`. |
| The second run still shows changes | Look for the task in the grid: a `command` task needs `changed_when`, and a password task needs `update_password: on_create`. |
