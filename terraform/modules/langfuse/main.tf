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

resource "kubernetes_namespace" "langfuse" {
  metadata {
    name = "langfuse"
    labels = {
      "app.kubernetes.io/managed-by" = "terraform"
    }
  }
}

resource "helm_release" "langfuse" {
  name       = "langfuse"
  repository = "https://langfuse.github.io/langfuse-k8s"
  chart      = "langfuse"
  version    = var.langfuse_version
  namespace  = kubernetes_namespace.langfuse.metadata[0].name

  # Headless bootstrap: seed org, project, and admin user on first start
  set_sensitive {
    name  = "langfuse.initOrg.name"
    value = var.org_name
  }

  set_sensitive {
    name  = "langfuse.initProject.name"
    value = var.project_name
  }

  set_sensitive {
    name  = "langfuse.initProject.secretKey"
    value = var.langfuse_secret_key
  }

  set_sensitive {
    name  = "langfuse.initProject.publicKey"
    value = var.langfuse_public_key
  }

  set_sensitive {
    name  = "langfuse.initUser.email"
    value = var.admin_email
  }

  set_sensitive {
    name  = "langfuse.initUser.password"
    value = var.admin_password
  }

  # Bundled PostgreSQL (bitnami subchart)
  set {
    name  = "postgresql.enabled"
    value = "true"
  }

  # Disable external DB when using bundled postgres
  set {
    name  = "langfuse.externalDatabase.enabled"
    value = "false"
  }

  # Default service is ClusterIP — expose via port-forward for local dev
  set {
    name  = "service.type"
    value = var.service_type
  }

  wait    = true
  timeout = 600
}
