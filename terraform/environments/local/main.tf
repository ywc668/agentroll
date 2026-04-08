terraform {
  required_version = ">= 1.9"

  required_providers {
    kind = {
      source  = "tehcyx/kind"
      version = "~> 0.6"
    }
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

# ---------------------------------------------------------------------------
# 1. Kind cluster
# ---------------------------------------------------------------------------
module "kind_cluster" {
  source       = "../../modules/kind-cluster"
  cluster_name = var.cluster_name
}

# ---------------------------------------------------------------------------
# 2. Provider configuration — must be wired after the cluster is created.
#    Both providers read from the kubeconfig file written by Kind.
# ---------------------------------------------------------------------------
provider "helm" {
  kubernetes {
    config_path = module.kind_cluster.kubeconfig_path
  }
}

provider "kubernetes" {
  config_path = module.kind_cluster.kubeconfig_path
}

# ---------------------------------------------------------------------------
# 3. Argo Rollouts
# ---------------------------------------------------------------------------
module "argo_rollouts" {
  source = "../../modules/argo-rollouts"

  argo_rollouts_version = var.argo_rollouts_version

  depends_on = [module.kind_cluster]
}

# ---------------------------------------------------------------------------
# 4. AgentRoll operator
# ---------------------------------------------------------------------------
module "agentroll" {
  source = "../../modules/agentroll"

  # Point at the local chart checked into the repo
  chart_path        = "${path.root}/../../../charts/agentroll"
  image_repository  = var.agentroll_image_repository
  image_tag         = var.agentroll_image_tag
  image_pull_policy = var.agentroll_image_pull_policy

  # Wire Langfuse credentials so the analysis runner can reach the trace store
  langfuse_host       = module.langfuse.langfuse_host
  langfuse_public_key = var.langfuse_public_key
  langfuse_secret_key = var.langfuse_secret_key

  # Explicit ordering: Argo Rollouts CRDs must exist before the operator starts
  argo_rollouts_ready = module.argo_rollouts.ready

  depends_on = [module.argo_rollouts]
}

# ---------------------------------------------------------------------------
# 5. Langfuse (trace storage for quality gates)
# ---------------------------------------------------------------------------
module "langfuse" {
  source = "../../modules/langfuse"

  langfuse_public_key = var.langfuse_public_key
  langfuse_secret_key = var.langfuse_secret_key
  admin_email         = var.langfuse_admin_email
  admin_password      = var.langfuse_admin_password

  depends_on = [module.kind_cluster]
}
