# kubetrace

**kubetrace** is a pod-centric Kubernetes troubleshooting CLI. It starts with one pod and builds an evidence-backed picture of its runtime health and related Kubernetes resources.

> Early development — v0.1 foundation. The implementation is being delivered incrementally.

## Implemented

- Go CLI built with Cobra.
- Kubernetes client using the current kubeconfig, optional kubeconfig path and context.
- Pod overview: phase, readiness, restarts, node, IP, QoS, age, ServiceAccount and labels.
- Container overview: image, state, readiness, restart count, resource requests and limits.
- Controller owner chain: Pod → ReplicaSet → Deployment.
- Inbound trace: selector-matching Services → EndpointSlices (legacy Endpoints fallback) → Pod, including backend readiness and service port mappings.
- Evidence-based HEALTHY / FAILED / UNKNOWN verdict.
- Terminal, JSON and Markdown output formats.

## v0.1 roadmap

- Ingress → Service path.
- Environment and ConfigMap reference discovery (names only; never Secret values).
- Pod DNS configuration.
- Current and previous container logs, grouped errors and dependency context.
- Events timeline: restarts, OOMKilled, probe failures and exit codes.
- Mounted ConfigMap, Secret and PVC status.
- NetworkPolicies selecting the pod (inventory only, not policy evaluation).
- Evidence and suggested fix for every check.

## Later

- DNS resolution from inside the pod (UNKNOWN when permissions prevent the check).
- Healthy-vs-broken diff.
- Gateway API HTTPRoute support.
- Node conditions and resource usage when metrics-server is available.
- NetworkPolicy blocked-by analysis and Hubble integration.

## Install / build

Requires Go 1.23+.

```bash
git clone https://github.com/Harry10799/kubetrace.git
cd kubetrace
go mod tidy
go build -o kubetrace ./cmd/kubetrace
```

## Usage

```bash
kubetrace trace POD_NAME -n NAMESPACE
kubetrace trace POD_NAME -n NAMESPACE --output json
kubetrace trace POD_NAME -n NAMESPACE --output markdown
kubetrace trace POD_NAME -n NAMESPACE --context CONTEXT_NAME
```

The caller needs permission to read the target pod, its ReplicaSet owner when applicable, Services, EndpointSlices (`discovery.k8s.io`) and legacy Endpoints in the namespace. Inbound tracing reports UNKNOWN when the required endpoint APIs cannot be inspected.

## Project layout

```
cmd/kubetrace/       CLI entry point
internal/cli/        Cobra commands and output formats
internal/kube/       Kubernetes client construction
internal/analyzer/   Pod inspection and evidence/verdict rules
```

## Design principles

1. **Pod-centric:** inspect one requested pod and follow only directly related resources.
2. **Evidence first:** verdicts are based on observed Kubernetes state.
3. **Safe by default:** read-only API calls; never print Secret values.
4. **Honest uncertainty:** unavailable permissions or unsupported checks produce UNKNOWN rather than guesses.
5. **No cluster-wide scan:** kubetrace complements tools such as Popeye; it does not replace them.
