# kubetrace

**kubetrace** is a pod-centric Kubernetes troubleshooting CLI. It follows the path into a pod, gathers related configuration and runtime evidence, and reports a verdict with actionable suggestions.

> v0.1 focuses on read-only Kubernetes API inspection. It does not modify cluster resources, expose Secret values, run cluster-wide scans, or make AI-generated guesses.

## v0.1 scope

- Pod overview: status, readiness, restarts, node, IP, QoS, age, owner chain, images, resources, probes and ServiceAccount.
- Inbound trace: Service → EndpointSlices/Endpoints → Pod, including selector evidence and port mapping.
- Best-effort outbound references from environment variables and ConfigMaps (names only; never Secret values).
- Pod DNS configuration, mounted volumes and ConfigMap/Secret/PVC references.
- Pod-selecting NetworkPolicies (inventory only; no policy evaluation).
- Events and current/previous container logs.
- Evidence-based HEALTHY / FAILED / UNKNOWN verdicts.
- Terminal, JSON and Markdown output.

Ingress tracing, deeper dependency correlation and complete log/event analysis are being built incrementally. v0.2 is planned to add in-pod DNS checks, healthy-vs-broken diffing, Gateway API HTTPRoute support, and node/resource context when metrics-server is available.

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
# Inspect a pod in the current kubeconfig context
kubetrace trace POD_NAME -n NAMESPACE

# Structured output
kubetrace trace POD_NAME -n NAMESPACE --output json
kubetrace trace POD_NAME -n NAMESPACE --output markdown

# Use a specific kubeconfig context
kubetrace trace POD_NAME -n NAMESPACE --context CONTEXT_NAME
```

The caller needs read permissions for the resources being inspected (Pods, their owner resources, Services, EndpointSlices/Endpoints, Events, logs, ConfigMaps, PVCs and NetworkPolicies). Missing optional permissions are reported as UNKNOWN where possible; Secret data is never printed.

## Project layout

```
cmd/kubetrace/       CLI entry point
internal/cli/        Cobra commands and output formats
internal/kube/       Kubernetes client construction
internal/analyzer/   Pod inspection and evidence/verdict rules
```

## Design principles

1. **Pod-centric:** start from one pod; follow only directly related resources.
2. **Evidence first:** every verdict is backed by observed Kubernetes state.
3. **Safe by default:** read-only API calls; never print Secret values.
4. **Honest uncertainty:** unavailable permissions or unsupported checks produce UNKNOWN, not guessed conclusions.
5. **No cluster-wide scan:** kubetrace is complementary to tools such as Popeye.

## Status

Early development — v0.1 foundation. APIs and output may evolve.
