# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| 0.1.x   | ✅ Yes     |

Older versions are not supported. Please upgrade to the latest release.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Report security vulnerabilities by opening a [GitHub Security Advisory](https://github.com/ywc668/agentroll/security/advisories/new).

You will receive a response within **48 hours** acknowledging the report.
If the issue is confirmed, a patch will be released as soon as possible — generally within **7 days** for critical issues.

Please include:
- A description of the vulnerability and its impact
- Steps to reproduce or a proof-of-concept
- The affected version(s)
- Any suggested mitigations (optional)

## Security Design

### Operator RBAC

AgentRoll follows the principle of least privilege. The operator's ClusterRole grants only the exact verbs needed per resource:

- **AgentDeployment CRD** — full lifecycle (create/update/delete)
- **Argo Rollouts/AnalysisTemplates** — full lifecycle (controller-managed)
- **Services, ConfigMaps, ServiceAccounts** — create/update/patch only (no delete on SA)
- **ReplicaSets** — read-only (for stable version tracking)
- **Events** — create/patch only
- **KEDA ScaledObjects** — full lifecycle (optional; graceful no-op when KEDA is absent)
- **Leases** — required for leader election

The operator does **not** have access to Secrets, Pods, or Deployments.

### Per-Agent ServiceAccounts

Each `AgentDeployment` automatically receives its own Kubernetes ServiceAccount (named after the agent). Agents run with this dedicated SA rather than the `default` SA, providing pod-level RBAC isolation.

### Validating Webhook

When `webhook.enabled: true` in the Helm chart, AgentRoll installs a `ValidatingWebhookConfiguration` that rejects invalid `AgentDeployment` specs at admission time. TLS for the webhook server is managed by cert-manager.

### Image Supply Chain

Production releases are:
- Built via GitHub Actions using pinned actions (SHA-pinned)
- Scanned with [Trivy](https://github.com/aquasecurity/trivy) for known CVEs
- Scanned with [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) for Go module vulnerabilities
- Signed with [cosign](https://github.com/sigstore/cosign) (keyless, Sigstore transparency log)

Verify an image signature:

```bash
cosign verify \
  --certificate-identity-regexp "https://github.com/ywc668/agentroll" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ghcr.io/ywc668/agentroll:<tag>
```

### HTTP/2

HTTP/2 is **disabled by default** on the metrics and webhook servers to mitigate CVE-2023-44487 (HTTP/2 Rapid Reset) and CVE-2023-39325 (HTTP/2 Stream Cancellation).

## Dependency Updates

Dependencies are updated regularly and `govulncheck` runs on every pull request. To check for vulnerabilities locally:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```
