resource "docker_network" "this" {
  name = var.name

  ipam_config {
    subnet = var.cidr
  }

  lifecycle {
    # The Docker provider (3.x, with Docker 29) reads the network back without
    # its subnet, so every plan would recreate the network. The subnet can't
    # change in place anyway: to move it, rename the network.
    ignore_changes = [ipam_config]
  }
}
