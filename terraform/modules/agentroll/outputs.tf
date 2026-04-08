output "namespace" {
  description = "Namespace where the AgentRoll operator is installed"
  value       = kubernetes_namespace.agentroll.metadata[0].name
}

output "release_name" {
  description = "Helm release name for the AgentRoll operator"
  value       = helm_release.agentroll.name
}

output "ready" {
  description = "Signals that the AgentRoll operator Helm release is deployed"
  value       = helm_release.agentroll.status
}
