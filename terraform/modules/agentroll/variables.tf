variable "chart_path" {
  description = "Path to the AgentRoll Helm chart directory, or an OCI registry reference (e.g. oci://ghcr.io/agentroll/helm-charts/agentroll)"
  type        = string
}

variable "image_repository" {
  description = "Container image repository for the AgentRoll operator"
  type        = string
  default     = "ghcr.io/agentroll/agentroll"
}

variable "image_tag" {
  description = "Container image tag for the AgentRoll operator"
  type        = string
  default     = "latest"
}

variable "image_pull_policy" {
  description = "Image pull policy for the AgentRoll operator container"
  type        = string
  default     = "IfNotPresent"
}

variable "argo_rollouts_ready" {
  description = <<-EOT
    Informational: pass module.argo_rollouts.ready here to document the
    intended dependency. Terraform cannot use variables in depends_on, so
    you must also set depends_on = [module.argo_rollouts] at the module
    call site to enforce the actual install ordering.
  EOT
  type        = string
  default     = null
}

variable "langfuse_host" {
  description = "In-cluster Langfuse service URL injected as LANGFUSE_HOST env var (e.g. http://langfuse.langfuse.svc.cluster.local:3000). Leave empty to skip."
  type        = string
  default     = ""
}

variable "langfuse_public_key" {
  description = "Langfuse project public key injected as LANGFUSE_PUBLIC_KEY env var. Leave empty to skip."
  type        = string
  default     = ""
  sensitive   = true
}

variable "langfuse_secret_key" {
  description = "Langfuse project secret key injected as LANGFUSE_SECRET_KEY env var. Leave empty to skip."
  type        = string
  default     = ""
  sensitive   = true
}
