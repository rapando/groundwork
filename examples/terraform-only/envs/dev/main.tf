terraform {
  required_version = ">= 1.5"

  backend "local" {
    path = "dev.tfstate"
  }
}

module "service" {
  source = "../../modules/service"

  name     = "${var.project}-dev"
  replicas = var.replicas
}
