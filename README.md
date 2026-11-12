# KTRX

**KTRX (Kubernetes Troubleshooting & Root-cause Explorer)** is an evidence-first Kubernetes troubleshooting CLI. Point it at a Pod or Deployment to inspect workload health, trace inbound Service/Ingress relationships, review recent events and logs, inspect declared egress policy, and compare a known-good Pod with a candidate.

KTRX helps answer **“what can we prove from the Kubernetes API right now?”** It reports findings with evidence and suggestions instead of guessing at a root cause.

> **Project status:** Early-stage / pre-1.0. Interfaces and diagnostics may change. Review the limitations below before using it in production.

## What it can do

- **Workload health:** Pod readiness and phase, container state, restarts, OOM/termination evidence, resource requests/limits, QoS and owner chain.
- **Inbound path:** Service selectors, EndpointSlices (with legacy Endpoints fallback), endpoint readiness, Service ports and Ingress host/path backends.
- **Ingress awareness:** IngressClass lookup and ingress-nginx controller discovery. Other controller implementations are identified as unsupported for controller-Pod discovery rather than treated as failed.
- **Outbound context:** Pod DNS configuration and NetworkPolicies selecting the Pod, including declared egress isolation, destinations and ports.
- **Events and logs:** Pod event timeline, common error patterns and HTTP 4xx/5xx access-log entries; optional full current/previous logs.
- **Dependency clues:** Correlates literal same-namespace Service references in container environment values/commands with selected backend Pod health.
- **Runtime checks:** Explicit DNS, TCP and HTTP(S) probes from an existing Pod container.
- **Comparison:** Compare a known-good Pod with a candidate to surface configuration and evidence differences.
- **Change timeline:** Compare retained Deployment-owned ReplicaSet revisions, Pod-template annotations/checksums, Deployment scaling events, Pod Warning events and restarts. Optionally read Flux Kustomization and HelmRelease history, including Git revisions. Referenced ConfigMap/Secret metadata is shown without displaying values.
- **Preflight:** Check Kubernetes API connectivity and the RBAC permissions KTRX uses.
- **Output:** Colorized terminal, JSON and Markdown (command-specific output support is noted below).

## Quick start

### Requirements

- Go **1.23+** to build or install from source.
- A Kubernetes cluster and a working kubeconfig (kubectl should connect to the intended cluster).
- Kubernetes permissions for the checks you want to run. See [RBAC permissions](#safety-and-rbac-permissions).

### Install from source

The repository is currently named `kubetrace`, while the installed executable is `ktrx`. Clone the repository URL that exists today, then install the local module:

~~~bash
git clone https://github.com/Harry10799/kubetrace.git
cd kubetrace
go install ./cmd/ktrx
~~~

Ensure `$(go env GOPATH)/bin` is in your PATH, then verify:

~~~bash
ktrx --help
~~~

To build a local binary instead:

~~~bash
go build -o ktrx ./cmd/ktrx
./ktrx --help
~~~

## Usage

KTRX uses your current kubeconfig context by default. The namespace defaults to default.

### 1. Inspect a Pod

~~~bash
ktrx nginx-abc123 -n production
~~~

Inspect full logs or choose a machine-readable format:

~~~bash
ktrx nginx-abc123 -n production --logs
ktrx nginx-abc123 -n production --output json
ktrx nginx-abc123 -n production --output markdown
~~~

### 2. Inspect a Deployment

By default, KTRX selects the least healthy matching Pod first (not-ready Pods, then higher restart counts):

~~~bash
ktrx deploy/api -n production
~~~

Inspect every matching replica:

~~~bash
ktrx deploy/api -n production --all
~~~

### 3. Check cluster access before troubleshooting

~~~bash
ktrx doctor -n production
~~~

Include the permission needed for runtime network checks:

~~~bash
ktrx doctor -n production --include-netcheck
~~~

The preflight uses Kubernetes SelfSubjectAccessReview. If the identity cannot create these review resources, permission results can be UNKNOWN; this does not mean the underlying resource permission is denied.

### 4. Run an explicit network check from a Pod

~~~bash
ktrx netcheck nginx-abc123 -n production --dns kubernetes.default.svc
ktrx netcheck nginx-abc123 -n production --tcp postgres.production.svc:5432
ktrx netcheck nginx-abc123 -n production --http https://api.example.com/health
~~~

You can request more than one probe in a single invocation:

~~~bash
ktrx netcheck nginx-abc123 -n production \
  --dns api.example.com \
  --tcp api.example.com:443 \
  --http https://api.example.com/health \
  --output json
~~~

The selected container must have /bin/sh and the relevant utility: getent or nslookup for DNS, nc for TCP, and curl or wget for HTTP. Use --container NAME for multi-container Pods. Each probe has a 10-second default timeout (maximum 2 minutes). Missing utilities are reported as unavailable, not as network failures.

### 5. Compare a known-good Pod with a candidate

~~~bash
ktrx compare api-good-abc api-bad-xyz -n production
~~~

**Argument order matters:** reference Pod first, candidate Pod second. Differences are investigation clues, not proof of causation.

### 6. Trace a URL to Kubernetes workloads

~~~bash
ktrx trace-url https://api.example.com/health -n production
ktrx trace-url https://api.example.com/health -n production --output json
ktrx trace-url https://api.example.com/health --all-namespaces --ingress-class alb
~~~

KTRX performs read-only route discovery and follows matching Kubernetes Ingress or Gateway API HTTPRoute rules to their Service backends, EndpointSlices and referenced Pods. Host and path matching is applied before resolving backends. Routes are ordered by host specificity and path precedence. The trace includes ready and not-ready EndpointSlice targets, Pod readiness/restarts, IngressClass, Ingress load-balancer hostnames and matching AWS TargetGroupBinding metadata when its CRD is installed. Use `--all-namespaces` to search cluster-wide or `--ingress-class CLASS` to filter Ingresses.

This is **configuration-path tracing**, not a live HTTP probe: it does not resolve public DNS, validate TLS, contact an external load balancer, or prove that a client can reach the application. Gateway API parent/listener attachment readiness and cross-namespace backend references are not verified yet. AWS TargetGroupBinding metadata is Kubernetes-side association only; AWS target health and unhealthy reasons are not queried by this command. HTTPRoute support is optional and requires the Gateway API CRD plus read permissions. If the API is absent or access is denied, KTRX reports a limitation rather than treating it as an application failure.

### 7. Inspect Deployment change history

~~~bash
ktrx timeline deploy/api -n production
ktrx timeline deploy/api -n production --output json
ktrx timeline deploy/api -n production --output markdown
~~~

The initial timeline implementation compares ReplicaSets still retained by Kubernetes and reports their revision timestamps and supported Pod-template changes (container images, environment variable names/references, and CPU/memory requests and limits). It filters ReplicaSets by the Deployment's controller owner reference, not labels alone. Environment variable values are never emitted. Kubernetes does not retain deleted ReplicaSets indefinitely, and ReplicaSet creation timestamps are not an audit log; the output explicitly reports these history limitations and does not claim causation.

### Common options

~~~bash
# Select kubeconfig and context
ktrx POD -n production --kubeconfig ~/.kube/config --context prod

# Include Normal Events
ktrx POD -n production --verbose

# Disable terminal colors
ktrx POD -n production --no-color
~~~

Use ktrx --help and ktrx <command> --help for the complete command and flag reference.

## Understanding verdicts

KTRX attaches a verdict to individual evidence items and produces a headline verdict:

| Verdict | Meaning |
| --- | --- |
| FAILED | A concrete failure was observed. |
| UNKNOWN | Important evidence could not be verified; this is not the same as healthy. |
| WARN | A risk, degraded condition or configuration concern was observed. |
| INFO | Context or an optional observation; not counted as a failure. |
| HEALTHY | The checked condition was observed to be healthy. |

Headline precedence:

~~~text
FAILED > UNKNOWN (critical evidence unavailable) > WARN > HEALTHY
~~~

Some explicitly optional observations can be UNKNOWN without downgrading the headline. They remain visible in the evidence output. A HEALTHY headline means the checks KTRX could perform did not identify a higher-priority issue; it is **not** a guarantee that the application is correct or reachable from every client.

## Safety and RBAC permissions

KTRX does not create diagnostic Pods or modify workload resources during normal inspection. The netcheck command is opt-in: it executes the requested commands inside an existing Pod container and can generate DNS queries or outbound traffic.

The caller needs read permissions for the resources used by the selected checks, including:

- Pods, Services, EndpointSlices and legacy Endpoints.
- Ingresses and IngressClasses; Gateway API HTTPRoutes (optional); AWS TargetGroupBindings (optional).
- NetworkPolicies, Events and pods/log.
- ReplicaSets for owner-chain inspection and Deployment timelines.
- ConfigMaps and Secrets through metadata-only reads for resourceVersion/managedFields inspection; Secret data is not fetched.
- Flux Kustomizations, HelmReleases, GitRepositories and Argo CD Applications (optional) for GitOps reconciliation and release history.
- Pod listing across namespaces for Ingress controller discovery.
- pods/exec create permission for ktrx netcheck.
- SelfSubjectAccessReview creation for ktrx doctor.

Use ktrx doctor -n NAMESPACE to inspect the required permissions. Grant only the permissions appropriate for your environment. KTRX does not read or print Secret values.

## Live demo

[View the captured KTRX command output](docs/DEMO.md). The report is generated from a real Kubernetes cluster and may reflect that cluster's RBAC, installed controllers and available container utilities.

To regenerate it in your own environment:

~~~bash
bash scripts/generate-demo.sh
~~~

The script requires Bash, Go, kubectl and a reachable cluster; Minikube is optional. It creates an isolated ktrx-demo namespace and removes it when finished. Set KEEP_DEMO=1 to retain the namespace for inspection. The script does not enable Minikube addons or change your cluster configuration. Review the script before running it against a shared cluster.

## Current limitations

KTRX is an aid for investigation, not an autonomous root-cause oracle.

- Dependency discovery is limited to literal same-namespace Service references in container environment values and command arguments. It does not resolve values stored only in Secrets/ConfigMaps or discover arbitrary cross-namespace/external dependencies.
- NetworkPolicy analysis describes declared policy configuration. Runtime probe failures can also be caused by DNS, routing, firewalls, TLS or application health; KTRX does not claim a failed probe proves NetworkPolicy is responsible.
- Runtime probes require suitable utilities inside the chosen container and execute in that container's network context.
- Ingress controller Pod discovery currently supports ingress-nginx. Other controllers can still have their Ingress routes inspected, but controller-Pod health discovery is not implemented for them.
- Kubernetes API visibility and RBAC restrictions can cause checks to be reported as UNKNOWN.
- Gateway API HTTPRoute discovery is supported, but parent/listener attachment and cross-namespace ReferenceGrant validation remain incomplete. Hubble integration is not currently implemented.

## Contributing

Bug reports, reproducible examples, test cases and focused pull requests are welcome. Please include:

1. KTRX version/commit and Kubernetes version.
2. The command you ran (redact credentials, tokens, internal hostnames and sensitive data).
3. Relevant output with --output json where possible.
4. Expected behavior versus actual behavior.

Please do not attach kubeconfig files, Secret manifests, access tokens or unredacted production logs.

## License

KTRX is licensed under the [Apache License 2.0](LICENSE).


## Deployment change timeline

KTRX can correlate retained rollout revisions with Kubernetes operational events:

```bash
ktrx timeline deploy/my-app -n production
ktrx timeline deploy/my-app -n production --output json
ktrx timeline deploy/my-app -n production --output markdown
ktrx timeline deploy/my-app -n production --since 6h
```

The timeline includes:

- ReplicaSet revision and Pod-template diffs, including annotations such as `checksum/config`.
- Deployment scaling events and relevant Pod Warning events when the Kubernetes Events API still retains them.
- Pod restart counts, retained previous-termination timestamps, and the earliest available failure evidence.
- Referenced ConfigMap and Secret metadata through the Kubernetes metadata-only API: resourceVersion, managedFields manager names, and timestamps. Secret payloads are never fetched. Missing required references are surfaced as failures.
- Optional Flux Kustomization/HelmRelease and Argo CD Application history, including Git revisions and GitHub/GitLab/Bitbucket commit links where resolvable.
- A Recent Deployment Changes section in `ktrx pod` reports for Deployment-owned Pods, including nearest-first potential correlations.
- Current-rollout failure evidence and nearest-first potential correlations to earlier rollout, config or GitOps changes. Correlations explicitly do not establish causation.

Flux support is read-only and optional. Missing CRDs or insufficient RBAC are reported as limitations rather than treated as application failures.

**History limitations:** The timeline defaults to the last 24 hours; change this with `--since` (for example, `--since 7d`). Kubernetes Events may expire according to cluster configuration. ReplicaSet history can be pruned or reused during rollback. ConfigMap/Secret resourceVersions are opaque identifiers; the metadata-only API provides current metadata and managedFields timestamps, not historical object versions or a full audit trail. GitOps revisions are linked only when workload labels or controller inventory identify the owning application. Failure start means earliest retained evidence for a currently unhealthy workload; correlations are temporal clues, not proof of causation.
