output "network_id" {
  value = module.network.network_id
}

output "endpoints" {
  description = "Where each server is reachable from this machine."
  value = {
    for name, c in docker_container.server : name => [for p in c.ports : "127.0.0.1:${p.external} -> ${p.internal}"]
  }
}

# Hosts for Ansible: one group per server. Containers have no SSH, so Ansible
# reaches them through the Docker connection plugin (community.docker).
output "ansible_hosts" {
  description = "Map of Ansible group to list of {name, ...host variables}."
  value = {
    for name, c in docker_container.server : name => [{
      name               = c.name # the container, which the Docker connection plugin connects to
      ansible_connection = "community.docker.docker"
    }]
  }
}
