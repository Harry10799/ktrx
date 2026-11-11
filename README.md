# KTRX

**KTRX** stands for **Kubernetes Troubleshooting & Root-cause Explorer**.

**KTRX** is a pod-centric Kubernetes troubleshooting CLI. Give it a Pod name and it builds an evidence-backed view of that Pod's health, ownership, inbound traffic path, outbound network policy and recent activity.

> **Development status:** early development (v0.2 diagnostics). KTRX is read-only. Checks that require an in-container probe or deeper application correlation are reported as `UNKNOWN` rather than guessed.

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
- Explicit, user-selected DNS, TCP and HTTP(S) probes can run inside an existing Pod container with `ktrx netcheck`.

NetworkPolicy allow rules are additive across all policies selecting a Pod. KTRX reports the declared policy configuration; it does not infer that a destination is blocked from one policy in isolation. Run `ktrx netcheck` from the workload Pod to gather runtime evidence. A failed probe alone does not prove NetworkPolicy is the cause; routing, DNS, firewalls, TLS and application health can also affect the result.

### Events and logs

- Pod Events timeline with timestamps, reasons, messages, counts and reporting source.
- Warning Events by default; use `--verbose` to include Normal Events.
- Log analysis for common error/fatal/exception patterns and HTTP 4xx/5xx responses.
- Use `--logs` to display full current and previous container logs.
- Cross-service dependency-failure correlation remains a future enhancement; KTRX does not infer upstream root causes from unrelated workloads.

### Output and Deployment inspection

- Colored terminal output with verdict-aware status colors; disable colors with `--no-color` or `NO_COLOR`.
- Terminal, JSON and Markdown output formats.
- Deployment targets select the least healthy matching Pod first (not-ready Pods, then higher restart counts).
- Use `--all` to inspect every matching replica. Shared findings are shown once, followed by Pod-specific differences.

## Install

**Requirements:** Go 1.23 or later and access to a Kubernetes cluster through a kubeconfig.

### Build from source

```bash
git clone https://github.com/Harry10799/ktrx.git
cd ktrx
go build -o ktrx ./cmd/ktrx
```

Run the local binary:

```bash
./ktrx --help
```

### Install with Go

```bash
go install ./cmd/ktrx
```

Go installs the binary into `$(go env GOPATH)/bin`. Add that directory to your `PATH` if the shell cannot find `ktrx`:

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
ktrx POD_NAME -n NAMESPACE

# Inspect a Deployment (select the least healthy matching Pod)
ktrx deploy/DEPLOYMENT_NAME -n NAMESPACE

# Inspect every Pod belonging to a Deployment
ktrx deploy/DEPLOYMENT_NAME -n NAMESPACE --all
```

### Options

```bash
# JSON or Markdown output
ktrx POD_NAME -n NAMESPACE --output json
ktrx POD_NAME -n NAMESPACE --output markdown

# Logs, verbose Events and color control
ktrx POD_NAME -n NAMESPACE --logs
ktrx POD_NAME -n NAMESPACE --verbose
ktrx POD_NAME -n NAMESPACE --no-color

# Use a specific Kubernetes context
ktrx POD_NAME -n NAMESPACE --context CONTEXT_NAME
```

### Cluster preflight

Check Kubernetes API connectivity and the read permissions KTRX uses before investigating a production workload:

```bash
ktrx doctor -n production
ktrx doctor -n production --output json
ktrx doctor -n production --timeout 20s
ktrx doctor -n production --include-netcheck
```

The command checks Pod, Service, EndpointSlice/Endpoints, Ingress/IngressClass, NetworkPolicy, Event, log, ReplicaSet and Ingress-controller discovery permissions. Use `--include-netcheck` to additionally verify the `create` permission on `pods/exec` needed by runtime probes. It is read-only and returns a non-zero exit status if any required permission is denied or cannot be verified.

KTRX uses Kubernetes SelfSubjectAccessReview for RBAC checks. If the identity cannot create SelfSubjectAccessReview resources, the permission results are reported as `UNKNOWN`; ask the cluster administrator to permit that self-review API or verify the listed permissions through the organization's approved process. A passing preflight confirms authorization checks only—it does not validate application health, network reachability or NetworkPolicy enforcement.

### Runtime network checks

Run only the probes you explicitly request, from a selected container in one existing Running Pod. KTRX does not create diagnostic Pods or modify workloads:

```bash
ktrx netcheck nginx-pod -n production --dns kubernetes.default.svc
ktrx netcheck nginx-pod -n production --tcp postgres.production.svc:5432
ktrx netcheck nginx-pod -n production --http https://api.example.com/health
ktrx netcheck nginx-pod -n production --dns api.example.com --tcp api.example.com:443 --http https://api.example.com/health --output json
```

The container must contain `/bin/sh` and the required utility: `getent` or `nslookup` for DNS, `nc` for TCP, and `curl` or `wget` for HTTP. Specify `--container` for multi-container Pods. Each probe has a 10-second default timeout; configure it with `--timeout` (maximum 2 minutes). Missing utilities are reported as `UNAVAILABLE`, not as network failures. HTTP 4xx/5xx responses are treated as failed HTTP checks. Probes execute inside the selected container and may generate DNS queries or outbound traffic; use only approved destinations.

### Compare Pods

Compare a known-good reference Pod with a candidate Pod to surface differences in health, container settings, resources, Services, Ingress routes, DNS, NetworkPolicies and diagnostic evidence:

```bash
ktrx compare REFERENCE_POD CANDIDATE_POD -n NAMESPACE
ktrx compare REFERENCE_POD CANDIDATE_POD -n NAMESPACE --output json
```

The first Pod is the reference; the second is the candidate being investigated. Differences are clues, not proof of root cause.

### Subcommands and compatibility

The explicit `pod` subcommand is also supported:

```bash
ktrx pod POD_NAME -n NAMESPACE
```

The older `trace` syntax remains available as a backward-compatible alias:

```bash
ktrx trace POD_NAME -n NAMESPACE
```

For full per-Pod logs when inspecting a Deployment, run the direct Pod command with `--logs`.

## Kubernetes permissions

KTRX uses the current kubeconfig, or an explicitly supplied kubeconfig/context. The caller needs read access to the resources used by the checks:

- Pods and, when applicable, ReplicaSets.
- Services, EndpointSlices (`discovery.k8s.io`) and legacy Endpoints.
- Ingresses and IngressClasses (`networking.k8s.io`).
- NetworkPolicies (`networking.k8s.io`) in the Pod namespace.
- Events and Pod logs (`pods/log`).
- For `ktrx netcheck`, create access to `pods/exec` in the selected namespace.
- Pods across namespaces for Ingress controller discovery.

Some checks may return `UNKNOWN` if the caller lacks the required permissions or the cluster does not expose the information. KTRX does not read or print Secret values.

## Project layout

```text
cmd/ktrx/       CLI entry point
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

- Cross-service dependency-failure correlation is not implemented.
- NetworkPolicy inspection is declarative; runtime checks provide path-specific evidence but cannot attribute a failure to NetworkPolicy alone.
- Gateway API HTTPRoute support and Hubble integration are future enhancements.
