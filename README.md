# KubeTrace

**KubeTrace** is a pod-centric Kubernetes troubleshooting CLI. Give it a Pod name and it builds an evidence-backed view of that Pod's health, ownership, inbound traffic path, outbound network policy and recent activity.

> **Development status:** early development (v0.2 diagnostics). KubeTrace is read-only. Checks that require an in-container probe or deeper application correlation are reported as `UNKNOWN` rather than guessed.

## Features

### Pod and workload health

- Pod phase, readiness, restart count, node, IP, QoS, age, ServiceAccount and labels.
- Container image, state, readiness, restart count, resource requests and limits.
- Owner chain: Pod → ReplicaSet → Deployment.
- Probe configuration checks. Missing readiness probes are WARN; missing liveness/startup probes are INFO.
- Evidence-based verdicts: `FAILED`, `WARN`, `UNKNOWN`, and `HEALTHY`.

### Inbound traffic

- Service selector matching and backend discovery through EndpointSlices, with legacy Endpoints fallback.
- Endpoint readiness and Service port → targetPort → declared container-port inspection.
- Ingress host/path backend tracing.
- IngressClass lookup, controller Pod discovery/readiness and load-balancer address status.

### Outbound diagnostics

- Pod DNS policy and configured nameservers, search domains and options.
- Namespace NetworkPolicies that select the Pod.
- Egress isolation status, declared destination selectors/CIDRs and ports.
- Live DNS resolution and outbound TCP/HTTP connectivity are **not** tested yet and remain `UNKNOWN`.

NetworkPolicy allow rules are additive across all policies selecting a Pod. KubeTrace reports the declared policy configuration; it does not infer that a destination is blocked from one policy in isolation. Actual enforcement also depends on the cluster's networking plugin (CNI).

### Events and logs

- Pod Events timeline with timestamps, reasons, messages, counts and reporting source.
- Warning Events by default; use `--verbose` to include Normal Events.
- Log analysis for common error/fatal/exception patterns and HTTP 4xx/5xx responses.
- Use `--logs` to display full current and previous container logs.
- Cross-service dependency-failure correlation is not implemented yet.

### Output and Deployment inspection

- Colored terminal output with verdict-aware status colors; disable colors with `--no-color` or `NO_COLOR`.
- Terminal, JSON and Markdown output formats.
- Deployment targets select the least healthy matching Pod first (not-ready Pods, then higher restart counts).
- Use `--all` to inspect every matching replica. Shared findings are shown once, followed by Pod-specific differences.

## Install

**Requirements:** Go 1.23 or later and access to a Kubernetes cluster through a kubeconfig.

### Build from source

```bash
git clone https://github.com/Harry10799/kubetrace.git
cd kubetrace
go build -o kubetrace ./cmd/kubetrace
```

Run the local binary:

```bash
./kubetrace --help
```

### Install with Go

```bash
go install ./cmd/kubetrace
```

Go installs the binary into `$(go env GOPATH)/bin`. Add that directory to your `PATH` if the shell cannot find `kubetrace`:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

Add the export to `~/.bashrc` or `~/.zshrc` to make it persistent.

### Install with Make

```bash
make build
make install
```

## Usage

The recommended syntax is to pass the Pod name directly:

```bash
# Inspect a Pod
kubetrace POD_NAME -n NAMESPACE

# Inspect a Deployment (select the least healthy matching Pod)
kubetrace deploy/DEPLOYMENT_NAME -n NAMESPACE

# Inspect every Pod belonging to a Deployment
kubetrace deploy/DEPLOYMENT_NAME -n NAMESPACE --all
```

### Options

```bash
# JSON or Markdown output
kubetrace POD_NAME -n NAMESPACE --output json
kubetrace POD_NAME -n NAMESPACE --output markdown

# Logs, verbose Events and color control
kubetrace POD_NAME -n NAMESPACE --logs
kubetrace POD_NAME -n NAMESPACE --verbose
kubetrace POD_NAME -n NAMESPACE --no-color

# Use a specific Kubernetes context
kubetrace POD_NAME -n NAMESPACE --context CONTEXT_NAME
```

### Compare Pods\n\nCompare a known-good reference Pod with a candidate Pod to surface differences in health, container settings, resources, Services, Ingress routes, DNS, NetworkPolicies and diagnostic evidence:\n\n```bash\nkubetrace compare REFERENCE_POD CANDIDATE_POD -n NAMESPACE\nkubetrace compare REFERENCE_POD CANDIDATE_POD -n NAMESPACE --output json\n```\n\nThe first Pod is the reference; the second is the candidate being investigated. Differences are clues, not proof of root cause.\n\n### Subcommands and compatibility

The explicit `pod` subcommand is also supported:

```bash
kubetrace pod POD_NAME -n NAMESPACE
```

The older `trace` syntax remains available as a backward-compatible alias:

```bash
kubetrace trace POD_NAME -n NAMESPACE
```

For full per-Pod logs when inspecting a Deployment, run the direct Pod command with `--logs`.

## Kubernetes permissions

KubeTrace uses the current kubeconfig, or an explicitly supplied kubeconfig/context. The caller needs read access to the resources used by the checks:

- Pods and, when applicable, ReplicaSets.
- Services, EndpointSlices (`discovery.k8s.io`) and legacy Endpoints.
- Ingresses and IngressClasses (`networking.k8s.io`).
- NetworkPolicies (`networking.k8s.io`) in the Pod namespace.
- Events and Pod logs (`pods/log`).
- Pods across namespaces for Ingress controller discovery.

Some checks may return `UNKNOWN` if the caller lacks the required permissions or the cluster does not expose the information. KubeTrace does not read or print Secret values.

## Project layout

```text
cmd/kubetrace/       CLI entry point
internal/cli/        Cobra commands and output formats
internal/kube/       Kubernetes client construction
internal/analyzer/   Pod inspection and evidence/verdict rules
```

## Design principles

1. **Pod-centric:** start with one Pod and follow directly related resources.
2. **Evidence first:** base findings on observed Kubernetes API state.
3. **Read-only by default:** do not mutate cluster resources.
4. **Honest uncertainty:** report unsupported or inaccessible checks as `UNKNOWN`.
5. **No cluster-wide scan:** complement tools such as Popeye rather than replace them.

## Current limitations

- Live DNS resolution and outbound TCP/HTTP probes are not implemented.
- Cross-service dependency-failure correlation is not implemented.
- NetworkPolicy inspection is declarative; runtime enforcement is not verified.
- Gateway API HTTPRoute support, healthy-vs-broken comparison, and Hubble integration are future enhancements.
