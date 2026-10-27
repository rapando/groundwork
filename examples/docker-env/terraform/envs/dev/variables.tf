variable "project" {
  description = "Project name used as a prefix."
  type        = string
}

variable "network_cidr" {
  description = "Subnet of the Docker network."
  type        = string
}

variable "servers" {
  description = "Servers to create, keyed by name (also its Ansible group), with the ports published on 127.0.0.1."
  type = map(object({
    ports = list(object({
      internal = number
      external = number
    }))
  }))
}
