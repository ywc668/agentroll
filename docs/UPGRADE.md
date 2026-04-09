# Upgrade Guide

This guide describes how to safely upgrade AgentRoll across versions.

## General Upgrade Process

AgentRoll uses Kubernetes server-side apply for CRD upgrades. This allows safe in-place field additions without disrupting existing `AgentDeployment` resources.

### Standard Upgrade (Helm)

```bash
# 1. Update the Helm repo
helm repo update

# 2. Upgrade the chart (CRDs are updated automatically when installCRDs=true)
helm upgrade agentroll agentroll/agentroll \
  --namespace agentroll-system \
  --reuse-values

# 3. Verify the controller is healthy
kubectl rollout status deployment/agentroll-controller-manager -n agentroll-system
kubectl get agentdeployments -A
```

### Upgrading CRDs Separately

If you manage CRDs separately from the operator (`installCRDs: false`), use the Makefile target:

```bash
# From the cloned repository
git pull
make upgrade-crds
```

This runs `kubectl apply --server-side --force-conflicts`, which safely merges CRD changes. It is equivalent to the Helm upgrade path and is safe to run against a live cluster.

### Manual CRD Upgrade

```bash
kubectl apply --server-side --force-conflicts \
  -f https://github.com/ywc668/agentroll/releases/download/<VERSION>/install.yaml
```

## Version-Specific Notes

### v0.1.x → v0.2.x (future)

No breaking changes in the `AgentDeployment` spec are planned for the v0.1 → v0.2 transition.

When breaking changes are introduced, this guide will include:
- Fields removed or renamed
- Required migration steps
- Rollback instructions

## Rollback

### Helm Rollback

```bash
helm rollback agentroll -n agentroll-system
```

### CRD Rollback

CRD rollbacks require care — removing fields from a CRD that are stored in existing objects can cause data loss.

For safe CRD rollback:
1. Delete all `AgentDeployment` resources that use the new fields
2. Apply the previous CRD version:
   ```bash
   kubectl apply --server-side -f config/crd/bases/agentroll.dev_agentdeployments.yaml
   ```
3. Roll back the operator deployment

## Verification After Upgrade

```bash
# Check CRD is at the expected version
kubectl get crd agentdeployments.agentroll.dev -o jsonpath='{.metadata.resourceVersion}'

# Verify existing AgentDeployments are still valid
kubectl get agentdeployments -A

# Check controller logs for reconcile errors
kubectl logs -n agentroll-system deployment/agentroll-controller-manager --tail=50

# Run a quick reconcile by annotating an AgentDeployment
kubectl annotate agentdeployment <name> upgrade-check/timestamp="$(date -u +%s)" --overwrite
```
