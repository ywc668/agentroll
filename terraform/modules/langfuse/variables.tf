variable "langfuse_version" {
  description = "Version of the Langfuse Helm chart to install"
  type        = string
  default     = "1.2.0"
}

variable "org_name" {
  description = "Name of the initial Langfuse organisation created on first boot"
  type        = string
  default     = "agentroll"
}

variable "project_name" {
  description = "Name of the initial Langfuse project created on first boot"
  type        = string
  default     = "agentroll-dev"
}

variable "langfuse_secret_key" {
  description = "Secret key (sk-...) seeded for the initial Langfuse project. Used by the AgentRoll analysis runner."
  type        = string
  sensitive   = true
}

variable "langfuse_public_key" {
  description = "Public key (pk-...) seeded for the initial Langfuse project. Used by the AgentRoll analysis runner."
  type        = string
  sensitive   = true
}

variable "admin_email" {
  description = "Email address for the initial Langfuse admin user"
  type        = string
  default     = "admin@agentroll.local"
}

variable "admin_password" {
  description = "Password for the initial Langfuse admin user"
  type        = string
  sensitive   = true
  default     = "changeme-local"
}

variable "service_type" {
  description = "Kubernetes Service type for the Langfuse web UI (ClusterIP, NodePort, or LoadBalancer)"
  type        = string
  default     = "ClusterIP"

  validation {
    condition     = contains(["ClusterIP", "NodePort", "LoadBalancer"], var.service_type)
    error_message = "service_type must be one of: ClusterIP, NodePort, LoadBalancer"
  }
}
