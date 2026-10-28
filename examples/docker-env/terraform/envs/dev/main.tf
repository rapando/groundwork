terraform {
  required_version = ">= 1.5"

  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
  }
}

locals {
  environment = "dev"
}

# Uses the local Docker daemon; set DOCKER_HOST (e.g. ssh://user@host) for a remote one.
provider "docker" {}

module "network" {
  source = "../../modules/network"

  name = "${var.project}-${local.environment}"
  cidr = var.network_cidr
}

# One image for every server (Debian with Python, see images/server): Terraform
# only creates the machines, Ansible decides what runs on each.
resource "docker_image" "server" {
  name = "${var.project}/server:bookworm"

  build {
    context = "${path.module}/../../images/server"
  }

  triggers = {
    dockerfile = filesha1("${path.module}/../../images/server/Dockerfile")
  }
}

resource "docker_container" "server" {
  for_each = var.servers

  name     = "${var.project}-${local.environment}-${each.key}"
  hostname = each.key
  image    = docker_image.server.image_id
  # Keeps the container running so Ansible can configure it.
  command = ["sleep", "infinity"]
  restart = "unless-stopped"

  networks_advanced {
    name    = module.network.network_name
    aliases = [each.key]
  }

  dynamic "ports" {
    for_each = each.value.ports
    content {
      internal = ports.value.internal
      external = ports.value.external
      ip       = "127.0.0.1"
    }
  }

  labels {
    label = "groundwork.role"
    value = each.key
  }
}
