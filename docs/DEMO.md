# KTRX live demo

This file is generated from a real Kubernetes cluster by `scripts/generate-demo.sh`.

It captures the commands and their actual output for:

- CLI help and build
- Pod inspection in terminal, JSON and Markdown
- Deployment replica inspection
- Kubernetes RBAC preflight
- Runtime DNS and TCP checks
- Reference-versus-candidate Pod comparison in terminal and JSON

Run the generator from the repository root:

```bash
bash scripts/generate-demo.sh
```

Requirements: Go, Bash, kubectl, and access to a Kubernetes cluster. The script creates an isolated `ktrx-demo` namespace and deletes it when finished. Use `KEEP_DEMO=1` to retain the namespace.

The generated report is intentionally based on live command output; results can differ depending on cluster permissions, Ingress controller availability, and runtime environment.
