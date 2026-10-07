variable "instance_count" {
  description = "Number of app servers."
  type        = number
}

variable "network" {
  description = "Second octet of the environment's 10.x.0.0/16 network."
  type        = number
}
