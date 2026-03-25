# CRD files go here.
# Copy from config/crd/bases/ after running `make manifests`:
#
#   cp config/crd/bases/agentroll.dev_agentdeployments.yaml charts/agentroll/crds/
#
# Helm automatically installs files in the crds/ directory
# before rendering any templates. CRDs in this directory:
# - Are installed on `helm install`
# - Are NOT upgraded on `helm upgrade` (Helm's safety policy)
# - Are NOT deleted on `helm uninstall`
#
# This is the correct behavior for CRDs — accidentally deleting
# a CRD would destroy all AgentDeployment resources in the cluster.
