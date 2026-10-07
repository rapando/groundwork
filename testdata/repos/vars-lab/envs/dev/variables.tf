variable "region" {
  type    = string
  default = "eu-west-1"
}

variable "replicas" {
  description = "How many app servers."
  type        = number
  validation {
    condition     = var.replicas > 0
    error_message = "Need at least one."
  }
}

variable "tags" {
  type    = map(string)
  default = { team = "core" }
}

variable "db_password" {
  type      = string
  sensitive = true
}

variable "alert_email" {
  type    = string
  default = null
}

variable "unused_flag" {
  type    = bool
  default = false
}
