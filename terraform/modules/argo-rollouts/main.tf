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

resource "kubernetes_namespace" "argo_rollouts" {
  metadata {
    name = "argo-rollouts"
    labels = {
      "app.kubernetes.io/managed-by" = "terraform"
    }
  }
}

resource "helm_release" "argo_rollouts" {
  name       = "argo-rollouts"
  repository = "https://argoproj.github.io/argo-helm"
  chart      = "argo-rollouts"
  version    = var.argo_rollouts_version
  namespace  = kubernetes_namespace.argo_rollouts.metadata[0].name

  set {
    name  = "installCRDs"
    value = "true"
  }

  set {
    name  = "controller.replicas"
    value = tostring(var.controller_replicas)
  }

  wait    = true
  timeout = 300
}
