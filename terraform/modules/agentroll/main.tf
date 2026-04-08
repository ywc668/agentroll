terraform {
  required_providers {
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.17"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
  }
}

resource "kubernetes_namespace" "agentroll" {
  metadata {
    name = "agentroll-system"
    labels = {
      "app.kubernetes.io/managed-by" = "terraform"
    }
  }
}

# Ordering: callers must set depends_on = [module.argo_rollouts] at the
# module call site so Argo Rollouts CRDs exist before this release runs.
# The argo_rollouts_ready variable is accepted for documentation purposes
# only; Terraform does not permit variables in depends_on blocks.
resource "helm_release" "agentroll" {
  name      = "agentroll"
  chart     = var.chart_path
  namespace = kubernetes_namespace.agentroll.metadata[0].name

  set {
    name  = "image.repository"
    value = var.image_repository
  }

  set {
    name  = "image.tag"
    value = var.image_tag
  }

  set {
    name  = "image.pullPolicy"
    value = var.image_pull_policy
  }

  # Point the operator at the Langfuse instance if a host is provided
  dynamic "set" {
    for_each = var.langfuse_host != "" ? [var.langfuse_host] : []
    content {
      name  = "env.LANGFUSE_HOST"
      value = set.value
    }
  }

  dynamic "set_sensitive" {
    for_each = var.langfuse_public_key != "" ? [var.langfuse_public_key] : []
    content {
      name  = "env.LANGFUSE_PUBLIC_KEY"
      value = set_sensitive.value
    }
  }

  dynamic "set_sensitive" {
    for_each = var.langfuse_secret_key != "" ? [var.langfuse_secret_key] : []
    content {
      name  = "env.LANGFUSE_SECRET_KEY"
      value = set_sensitive.value
    }
  }

  wait    = true
  timeout = 300
}
