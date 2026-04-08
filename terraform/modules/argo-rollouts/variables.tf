variable "argo_rollouts_version" {
  description = "Version of the Argo Rollouts Helm chart to install"
  type        = string
  default     = "2.39.5"
}

variable "controller_replicas" {
  description = "Number of Argo Rollouts controller replicas"
  type        = number
  default     = 1
}
