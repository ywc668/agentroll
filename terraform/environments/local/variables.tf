variable "cluster_name" {
  description = "Name of the local Kind cluster"
  type        = string
  default     = "agentroll-dev"
}

variable "argo_rollouts_version" {
  description = "Argo Rollouts Helm chart version"
  type        = string
  default     = "2.39.5"
}

variable "agentroll_image_repository" {
  description = "Container image repository for the AgentRoll operator"
  type        = string
  default     = "ghcr.io/agentroll/agentroll"
}

variable "agentroll_image_tag" {
  description = "Container image tag for the AgentRoll operator"
  type        = string
  default     = "latest"
}

variable "agentroll_image_pull_policy" {
  description = "Image pull policy for the AgentRoll operator (use IfNotPresent for locally-loaded images)"
  type        = string
  default     = "IfNotPresent"
}

variable "langfuse_public_key" {
  description = "Langfuse project public key (pk-...) seeded on first boot and injected into the operator"
  type        = string
  default     = "pk-agentroll-local"
}

variable "langfuse_secret_key" {
  description = "Langfuse project secret key (sk-...) seeded on first boot and injected into the operator"
  type        = string
  sensitive   = true
  default     = "sk-agentroll-local"
}

variable "langfuse_admin_email" {
  description = "Email address for the initial Langfuse admin user"
  type        = string
  default     = "admin@agentroll.local"
}

variable "langfuse_admin_password" {
  description = "Password for the initial Langfuse admin user"
  type        = string
  sensitive   = true
  default     = "changeme-local"
}
