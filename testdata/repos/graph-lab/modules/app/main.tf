variable "subnet_ids" {
  type = list(string)
}

variable "ami" {
  type = string
}

variable "replicas" {
  type    = number
  default = 1
}

resource "aws_instance" "web" {
  count         = var.replicas
  ami           = var.ami
  instance_type = "t3.small"
  subnet_id     = var.subnet_ids[0]
}

output "ip" {
  value = aws_instance.web[0].private_ip
}
