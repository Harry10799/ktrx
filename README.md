# kubetrace

**kubetrace** is a pod-centric Kubernetes troubleshooting CLI. It starts with one pod and builds an evidence-backed picture of its runtime health and related Kubernetes resources.

> Early development — v0.2 diagnostics. Some checks intentionally remain UNKNOWN until active in-container network and application analysis is implemented.

## Implemented

- Go CLI built with Cobra.
- Kubernetes client using the current kubeconfig, optional kubeconfig path and context.
- Pod overview: phase, readiness, restarts, node, IP, QoS, age, ServiceAccount and labels.
- Container overview: image, state, readiness, restart count, resource requests and limits.
- Controller owner chain: Pod → ReplicaSet → Deployment.
- Inbound trace: selector-matching Services → EndpointSlices (legacy Endpoints fallback) → Pod, including backend readiness and service port mappings.
- Ingress trace: matching Ingress host/path rules → backend Services selecting the Pod.
- Pod Events timeline, ordered by timestamp, including reason, message, count and reporting source.
- Log analysis: detect common error/fatal/exception patterns and HTTP 4xx/5xx lines; `--logs` displays full current and previous logs. Cross-service dependency-failure correlation is not implemented yet.
- Warning Events by default; include Normal Events with `--verbose`.
- Relative timestamps (for example, `31m ago`) in terminal and Markdown output.
- Deployment replica selection prioritizes not-ready Pods and higher restart counts; `--all` inspects every matching replica. Terminal output deduplicates shared findings and shows only per-Pod differences.
- Service targetPort and probe-port checks against declared container ports; undeclared ports remain UNKNOWN rather than being assumed broken, while a mismatch against declared ports is WARN.
- Evidence-based verdict precedence: FAILED > WARN > UNKNOWN > HEALTHY, with a summary of warning, unchecked and failed findings.
- Explicit UNKNOWN findings for checks not performed (DNS resolution, outbound connectivity and cross-service dependency-failure correlation). Missing readiness probes are WARN; missing liveness/startup probes are INFO.
- Ingress diagnostics: IngressClass existence, controller Pod discovery/readiness and assigned load-balancer addresses.
- Colored terminal output with verdict-aware status colors and a `--no-color` / `NO_COLOR` option.
- Terminal, JSON and Markdown output formats.

## v0.1 roadmap

- Environment and ConfigMap reference discovery (names only; never Secret values).
- Pod DNS configuration.
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

# Install into $(go env GOPATH)/bin (add it to PATH if needed)
go install ./cmd/kubetrace

# Or use the Makefile
make install
```

## Usage

If `kubetrace` returns `command not found`, add the Go bin directory to your PATH (for example, `export PATH="$(go env GOPATH)/bin:$PATH"`; persist it in `~/.bashrc` or `~/.zshrc`). The shortest form is the recommended command. The `pod` subcommand is also available, and Deployment targets resolve to one matching Pod.

```bash
# Inspect a Pod
kubetrace POD_NAME -n NAMESPACE

# Explicit Pod subcommand
kubetrace pod POD_NAME -n NAMESPACE

# Inspect one Pod selected from a Deployment
kubetrace deploy/DEPLOYMENT_NAME -n NAMESPACE

# Output formats and options
kubetrace POD_NAME -n NAMESPACE --output json
kubetrace POD_NAME -n NAMESPACE --output markdown
kubetrace POD_NAME -n NAMESPACE --no-color
kubetrace POD_NAME -n NAMESPACE --logs
kubetrace POD_NAME -n NAMESPACE --verbose
kubetrace deploy/DEPLOYMENT_NAME -n NAMESPACE --all
kubetrace POD_NAME -n NAMESPACE --context CONTEXT_NAME
```

The older `kubetrace trace POD_NAME` syntax remains supported as a backward-compatible alias, but the direct Pod form above is preferred.

The caller needs permission to read the target pod, its ReplicaSet owner when applicable, Services, EndpointSlices (`discovery.k8s.io`), legacy Endpoints, Ingresses (`networking.k8s.io`) and Events in the namespace, plus permission to read pod logs (`pods/log`). Ingress diagnostics also need `get` on cluster-scoped IngressClasses and `list` on Pods across namespaces to discover controller Pods. Event and log retrieval failures are reported as UNKNOWN evidence; warning Events are displayed as diagnostic context and do not independently change the overall verdict. Deployment targets select the least healthy matching Pod first (not-ready, then higher restart count); `--all` inspects every matching replica.

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
