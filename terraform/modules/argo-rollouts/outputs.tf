output "ready" {
  description = "Signals that Argo Rollouts is installed and ready. Use as a depends_on trigger for downstream modules."
  value       = helm_release.argo_rollouts.status
}

output "namespace" {
  description = "Namespace where Argo Rollouts is installed"
  value       = kubernetes_namespace.argo_rollouts.metadata[0].name
}

output "version" {
  description = "Installed Argo Rollouts Helm chart version"
  value       = helm_release.argo_rollouts.version
}
