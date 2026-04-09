# Contributing to AgentRoll

First off, thank you for considering contributing to AgentRoll! This project is in its early stages, and your input — whether it's code, ideas, bug reports, or documentation — can have a huge impact on its direction.

## How to Contribute

### Reporting Bugs

Open a [GitHub Issue](https://github.com/ywc668/agentroll/issues) with:
- A clear title and description
- Steps to reproduce
- Expected vs actual behavior
- Your environment (K8s version, Argo Rollouts version, OS)

### Suggesting Features

Open a [GitHub Discussion](https://github.com/ywc668/agentroll/discussions) in the "Ideas" category. Describe:
- The problem you're trying to solve
- Your proposed solution
- Any alternatives you've considered

### Submitting Code

1. Fork the repo
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Run tests (`make test`)
5. Run linting (`make lint`)
6. Commit with a clear message (`git commit -m "feat: add hallucination rate analysis template"`)
7. Push to your fork (`git push origin feature/my-feature`)
8. Open a Pull Request

### Commit Message Convention

We follow [Conventional Commits](https://www.conventionalcommits.org/):

- `feat:` — new feature
- `fix:` — bug fix
- `docs:` — documentation only
- `test:` — adding or updating tests
- `refactor:` — code change that neither fixes a bug nor adds a feature
- `chore:` — build process or auxiliary tool changes

## Development Setup

### Prerequisites

- Go 1.25+
- kubectl
- A Kubernetes cluster (minikube, kind, or remote)
- [Argo Rollouts](https://argoproj.github.io/argo-rollouts/installation/) installed on the cluster
- Docker (for building images)

### Getting Started

```bash
# Clone your fork
git clone https://github.com/<your-username>/agentroll.git
cd agentroll

# Install dependencies
make setup

# Run tests
make test

# Build the operator
make build

# Run locally against your cluster
make run
```

### Project Structure

```
agentroll/
├── api/
│   └── v1alpha1/          # CRD type definitions
├── cmd/
│   └── main.go            # Operator entrypoint
├── internal/
│   ├── controller/        # Reconciliation logic
│   ├── analysis/          # Agent-specific analysis providers
│   └── rollout/           # Argo Rollouts integration
├── config/
│   ├── crd/               # Generated CRD manifests
│   ├── rbac/              # RBAC permissions
│   └── samples/           # Example AgentDeployment YAMLs
├── charts/
│   └── agentroll/         # Helm chart
├── docs/
│   ├── adr/               # Architecture Decision Records
│   └── guides/            # User guides
├── templates/
│   └── analysis/          # Pre-built AnalysisTemplate library
├── hack/                  # Development scripts
├── Makefile
├── Dockerfile
├── README.md
├── CONTRIBUTING.md
└── LICENSE
```

## Good First Issues

Look for issues labeled [`good first issue`](https://github.com/ywc668/agentroll/labels/good%20first%20issue). These are specifically chosen to be approachable for newcomers.

## Code of Conduct

Be kind, be respectful. We're all here because we believe AI agents deserve better deployment infrastructure.

## Questions?

Open a [Discussion](https://github.com/ywc668/agentroll/discussions) — no question is too basic.
