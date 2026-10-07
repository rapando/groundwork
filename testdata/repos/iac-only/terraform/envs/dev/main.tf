provider "aws" {
  region = "eu-west-1"
}

module "network" {
  source = "../../modules/network"
  cidr   = var.cidr
}
