output "langfuse_host" {
  description = "In-cluster URL for the Langfuse API and UI"
  value       = "http://langfuse.${kubernetes_namespace.langfuse.metadata[0].name}.svc.cluster.local:3000"
}

output "namespace" {
  description = "Namespace where Langfuse is installed"
  value       = kubernetes_namespace.langfuse.metadata[0].name
}

output "port_forward_command" {
  description = "kubectl command to reach the Langfuse UI from localhost"
  value       = "kubectl port-forward -n ${kubernetes_namespace.langfuse.metadata[0].name} svc/langfuse 3000:3000"
}
