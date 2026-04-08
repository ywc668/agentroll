output "kubeconfig" {
  description = "Raw kubeconfig content for the Kind cluster"
  value       = kind_cluster.this.kubeconfig
  sensitive   = true
}

output "kubeconfig_path" {
  description = "Path to the kubeconfig file on disk"
  value       = kind_cluster.this.kubeconfig_path
}

output "cluster_name" {
  description = "Name of the created Kind cluster"
  value       = kind_cluster.this.name
}
