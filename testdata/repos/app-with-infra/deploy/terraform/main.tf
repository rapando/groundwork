locals {
  environment = terraform.workspace
}

# Stand-in for real servers (aws_instance, hcloud_server, ...).
resource "terraform_data" "app" {
  count = var.instance_count

  input = {
    name = "app-${local.environment}-${count.index + 1}"
    ip   = "10.${var.network}.0.${count.index + 10}"
  }
}
