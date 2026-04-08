# AgentRoll — Terraform Bootstrap

Terraform modules for bootstrapping a complete local AgentRoll development environment, or as a reference for production cluster bring-up.

## What gets installed

| Component | Version | Namespace |
|---|---|---|
| Kind cluster (local) | via `tehcyx/kind ~> 0.6` | — |
| Argo Rollouts | Helm chart 2.39.5 | `argo-rollouts` |
| AgentRoll operator | charts/agentroll (local) | `agentroll-system` |
| Langfuse v2 | Helm chart 1.2.0 | `langfuse` |

Langfuse ships with a bundled PostgreSQL (bitnami subchart) and is seeded with an initial organisation, project, and admin user on first boot. The public/secret key pair is automatically injected into the AgentRoll operator so the analysis runner can query traces for quality gates.

## Prerequisites

| Tool | Minimum version | Install |
|---|---|---|
| Terraform | >= 1.9 | https://developer.hashicorp.com/terraform/install |
| Kind | any | `brew install kind` |
| kubectl | any | `brew install kubectl` |
| Helm | >= 3.14 | `brew install helm` |
| Docker | running daemon | https://docs.docker.com/get-docker/ |

## Quick start

```bash
# 1. Navigate to the local environment
cd terraform/environments/local

# 2. Copy and (optionally) customise variables
cp terraform.tfvars.example terraform.tfvars
# edit terraform.tfvars — at minimum change the Langfuse passwords for any
# environment that is reachable from a network.

# 3. Initialise providers
terraform init

# 4. Preview the plan
terraform plan

# 5. Apply (takes ~5 minutes for cluster + Langfuse to become ready)
terraform apply
```

The apply takes roughly **3–6 minutes** on a standard laptop. Langfuse accounts for most of that time (PostgreSQL init + migrations).

## Connecting after apply

### kubeconfig

```bash
export KUBECONFIG=$(terraform output -raw kubeconfig_path)
kubectl get nodes
```

### Langfuse UI

Langfuse is exposed as a `ClusterIP` service inside the cluster. Port-forward to reach it locally:

```bash
$(terraform output -raw langfuse_port_forward)
# → kubectl port-forward -n langfuse svc/langfuse 3000:3000
```

Then open http://localhost:3000 and log in with the email/password from your `terraform.tfvars`.

### Langfuse API keys

```bash
terraform output langfuse_public_key
# secret key is sensitive — retrieve it from your tfvars or Terraform state
```

## Installing a locally-built operator image

Kind cannot pull from a remote registry by default. Load a locally-built image into the cluster first, then set `agentroll_image_pull_policy = "Never"`:

```bash
# Build
make docker-build IMG=ghcr.io/agentroll/agentroll:dev

# Load into Kind
kind load docker-image ghcr.io/agentroll/agentroll:dev --name agentroll-dev

# Apply with the dev tag
terraform apply \
  -var agentroll_image_tag=dev \
  -var agentroll_image_pull_policy=Never
```

## Cleanup

```bash
terraform destroy
```

This removes the Kind cluster and all resources. All data (Langfuse traces, Postgres) is lost.

## Module reference

### `modules/kind-cluster`

Creates a 1 control-plane + 1 worker Kind cluster.

| Variable | Default | Description |
|---|---|---|
| `cluster_name` | `agentroll-dev` | Name of the Kind cluster |

| Output | Description |
|---|---|
| `kubeconfig` | Raw kubeconfig (sensitive) |
| `kubeconfig_path` | Path to kubeconfig file on disk |
| `cluster_name` | Cluster name |

---

### `modules/argo-rollouts`

Installs Argo Rollouts via the official Helm chart, including CRDs.

| Variable | Default | Description |
|---|---|---|
| `argo_rollouts_version` | `2.39.5` | Helm chart version |
| `controller_replicas` | `1` | Number of controller replicas |

| Output | Description |
|---|---|
| `ready` | Release status string (use as `depends_on` input) |
| `namespace` | Installed namespace |
| `version` | Chart version |

---

### `modules/agentroll`

Installs the AgentRoll operator from a local Helm chart path or OCI registry.

| Variable | Default | Description |
|---|---|---|
| `chart_path` | — (required) | Local path or OCI ref to the Helm chart |
| `image_repository` | `ghcr.io/agentroll/agentroll` | Operator image repository |
| `image_tag` | `latest` | Operator image tag |
| `image_pull_policy` | `IfNotPresent` | Image pull policy |
| `argo_rollouts_ready` | `null` | Pass `module.argo_rollouts.ready` for ordering |
| `langfuse_host` | `""` | Injected as `LANGFUSE_HOST` env var (optional) |
| `langfuse_public_key` | `""` | Injected as `LANGFUSE_PUBLIC_KEY` env var (optional) |
| `langfuse_secret_key` | `""` | Injected as `LANGFUSE_SECRET_KEY` env var (optional) |

| Output | Description |
|---|---|
| `namespace` | Installed namespace |
| `release_name` | Helm release name |
| `ready` | Release status string |

---

### `modules/langfuse`

Deploys Langfuse v2 with bundled PostgreSQL, seeded with a default organisation, project, and admin user.

| Variable | Default | Description |
|---|---|---|
| `langfuse_version` | `1.2.0` | Helm chart version |
| `org_name` | `agentroll` | Initial organisation name |
| `project_name` | `agentroll-dev` | Initial project name |
| `langfuse_secret_key` | — (required) | Project secret key |
| `langfuse_public_key` | — (required) | Project public key |
| `admin_email` | `admin@agentroll.local` | Admin user email |
| `admin_password` | `changeme-local` | Admin user password |
| `service_type` | `ClusterIP` | Kubernetes Service type |

| Output | Description |
|---|---|
| `langfuse_host` | In-cluster service URL |
| `namespace` | Installed namespace |
| `port_forward_command` | Full `kubectl port-forward` command |
