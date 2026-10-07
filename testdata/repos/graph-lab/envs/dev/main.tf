provider "aws" {
  region = "eu-west-1"
}

resource "aws_vpc" "main" {
  cidr_block = var.cidr
}

resource "aws_subnet" "private" {
  count      = var.subnets
  vpc_id     = aws_vpc.main.id
  cidr_block = cidrsubnet(var.cidr, 8, count.index)
}

resource "aws_internet_gateway" "gw" {
  vpc_id = aws_vpc.main.id
}

data "aws_ami" "ubuntu" {
  most_recent = true
}

module "app" {
  source     = "../../modules/app"
  subnet_ids = aws_subnet.private[*].id
  ami        = data.aws_ami.ubuntu.id
  replicas   = 2
}

resource "aws_route53_record" "app" {
  name       = "app"
  type       = "A"
  records    = [module.app.ip]
  depends_on = [aws_internet_gateway.gw]
}

resource "aws_iam_role" "ops" {
  name = "ops"
}
