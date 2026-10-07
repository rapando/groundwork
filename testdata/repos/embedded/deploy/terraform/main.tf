provider "aws" {
  region = "eu-west-1"
}

resource "aws_instance" "app" {
  ami           = var.ami
  instance_type = "t3.small"
}
