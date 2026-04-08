output "kubeconfig_path" {
  description = "Path to the kubeconfig file for the local Kind cluster"
  value       = module.kind_cluster.kubeconfig_path
}

output "cluster_name" {
  description = "Name of the local Kind cluster"
  value       = module.kind_cluster.cluster_name
}

output "langfuse_host" {
  description = "In-cluster Langfuse service URL (accessible from within the cluster)"
  value       = module.langfuse.langfuse_host
}

output "langfuse_public_key" {
  description = "Langfuse project public key"
  value       = var.langfuse_public_key
}

output "langfuse_port_forward" {
  description = "Command to expose the Langfuse UI locally on http://localhost:3000"
  value       = module.langfuse.port_forward_command
}

output "agentroll_namespace" {
  description = "Namespace where the AgentRoll operator is running"
  value       = module.agentroll.namespace
}

output "argo_rollouts_namespace" {
  description = "Namespace where Argo Rollouts is running"
  value       = module.argo_rollouts.namespace
}
