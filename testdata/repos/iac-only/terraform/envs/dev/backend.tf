terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
  backend "s3" {
    bucket = "acme-tfstate"
    key    = "dev/terraform.tfstate"
    region = "eu-west-1"
  }
}
