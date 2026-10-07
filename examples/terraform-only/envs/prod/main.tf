terraform {
  required_version = ">= 1.5"

  backend "local" {
    path = "prod.tfstate"
  }
}

module "service" {
  source = "../../modules/service"

  name     = "${var.project}-prod"
  replicas = var.replicas
}
