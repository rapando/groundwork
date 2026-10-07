variable "name" {
  description = "Service name."
  type        = string
}

variable "replicas" {
  description = "Number of instances."
  type        = number
  default     = 1
}

variable "port" {
  description = "Listen port."
  type        = number
  default     = 8080
}
