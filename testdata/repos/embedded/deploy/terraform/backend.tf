terraform {
  backend "s3" {
    bucket = "app-tfstate"
    key    = "app.tfstate"
    region = "eu-west-1"
  }
}
