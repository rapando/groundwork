output "environment" {
  value = local.environment
}

# Read by ops/ansible/inventory/terraform.py
output "ansible_hosts" {
  description = "Map of Ansible group to list of {name, ip}."
  value = {
    app = [for h in terraform_data.app : { name = h.input.name, ip = h.input.ip }]
  }
}
