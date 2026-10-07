resource "terraform_data" "service" {
  count = var.replicas

  input = {
    name = "${var.name}-${count.index}"
    port = var.port
  }
}
