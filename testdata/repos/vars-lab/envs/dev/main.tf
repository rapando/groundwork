resource "terraform_data" "app" {
  input = {
    region   = var.region
    replicas = var.replicas
    tags     = var.tags
    email    = var.alert_email
  }
}

resource "terraform_data" "db" {
  input = var.db_password
}
