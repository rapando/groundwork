output "instances" {
  description = "Names of the instances."
  value       = [for s in terraform_data.service : s.input.name]
}
