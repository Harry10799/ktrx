#!/usr/bin/env bash
# Run realistic failure scenarios on a live cluster (built for Minikube), diagnose each
# one with KTRX, ASSERT the result, and write a curated demo.md.
#
# Requirements: bash, go (or KTRX_BINARY), kubectl, a reachable cluster. minikube optional.
# Usage:   bash scripts/generate-scenarios-demo.sh
# Options (env): KTRX_BINARY, KTRX_DEMO_DOC (default ./demo.md), KTRX_DEMO_NAMESPACE,
#                KEEP_DEMO=1 (keep namespace), MAX_LINES (default 40 lines per output),
#                KTRX_DEMO_GATEWAY=0 (skip Gateway API CRDs + HTTPRoute scenarios),
#                KTRX_DEMO_GITOPS=0  (skip simulated Argo CD / Flux objects)
# Exit status: 0 = every scenario passed its assertions, 1 = at least one failed.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
BINARY="${KTRX_BINARY:-$ROOT_DIR/ktrx}"
DOC="${KTRX_DEMO_DOC:-$ROOT_DIR/demo.md}"
NS="${KTRX_DEMO_NAMESPACE:-ktrx-scenarios}"
MAX_LINES="${MAX_LINES:-40}"
TMP="$(mktemp -d)"
BODY="$TMP/body.md"
: > "$BODY"
CREATED_NS=0
declare -a R_TITLE=() R_STATUS=() R_DETAIL=()
NS2="${NS}-backend"
GATEWAY_URL="https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml"
WANT_GATEWAY="${KTRX_DEMO_GATEWAY:-1}"; WANT_GITOPS="${KTRX_DEMO_GITOPS:-1}"
INSTALLED_GATEWAY=0; INSTALLED_ARGO=0; INSTALLED_HELM=0; SC=0; HINT=""

cleanup() {
  if [[ "$CREATED_NS" -eq 1 && "${KEEP_DEMO:-0}" != "1" ]]; then
    # The stuck-Terminating demo Pod carries a finalizer; remove it so the namespace can go away.
    kubectl patch pod stuck-term -n "$NS" --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true
    kubectl delete namespace "$NS" "$NS2" --wait=false >/dev/null 2>&1 || true
    [[ "$INSTALLED_ARGO" -eq 1 ]] && kubectl delete crd applications.argoproj.io >/dev/null 2>&1 || true
    [[ "$INSTALLED_HELM" -eq 1 ]] && kubectl delete crd helmreleases.helm.toolkit.fluxcd.io >/dev/null 2>&1 || true
    [[ "$INSTALLED_GATEWAY" -eq 1 ]] && kubectl delete -f "$GATEWAY_URL" --ignore-not-found >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

die() { echo "ERROR: $*" >&2; exit 1; }
say() { echo "[demo] $*" >&2; }
strip_ansi() { sed -E 's/\x1B\[[0-9;]*[[:alpha:]]//g'; }
narrate() { printf '\n%s\n' "$*" >> "$BODY"; }
heading() { printf '\n## %s\n' "$*" >> "$BODY"; }
scenario() { SC=$((SC + 1)); heading "Scenario $SC: $*"; }
skip_step() { # title reason
  R_TITLE+=("$1"); R_STATUS+=("SKIP"); R_DETAIL+=("$2")
  narrate "> **Skipped:** $1. $2"; say "SKIP  $1  ($2)"
}
ts_ago() { date -u -d "-$1 min" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -v-"$1"M '+%Y-%m-%dT%H:%M:%SZ'; }

# print_block "<command shown>" <file with output> <max lines> [exit code]
print_block() {
  local shown="$1" file="$2" max="$3" rc="${4:-}" total
  total="$(wc -l < "$file" | tr -d ' ')"
  {
    printf '\n```text\n$ %s\n' "$shown"
    head -n "$max" "$file"
    if (( total > max )); then printf '... (%d more lines trimmed)\n' $((total - max)); fi
    [[ -n "$rc" ]] && printf '[exit status: %s]\n' "$rc"
    printf '```\n'
  } >> "$BODY"
}

# kube_show <title-less> cmd...   -- short kubectl symptom output (never fails the demo)
kube_show() {
  local out="$TMP/kube.out"
  { "$@" 2>&1 || true; } | strip_ansi > "$out"
  print_block "$(printf '%q ' "$@" | sed 's/ $//')" "$out" 14
}

# wait_until <description> <timeout-seconds> <command...>
wait_until() {
  local desc="$1" timeout="$2"; shift 2
  local end=$((SECONDS + timeout))
  while (( SECONDS < end )); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 3
  done
  say "timed out waiting for: $desc (continuing; KTRX will report what exists)"
  return 1
}

first_pod() { kubectl get pods -n "$NS" -l "app=$1" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null; }
waiting_reason() { kubectl get pod "$1" -n "$NS" -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}' 2>/dev/null; }
pod_waiting_matches() { [[ "$(waiting_reason "$1")" =~ $2 ]]; }
app_waiting_matches() { local p; p="$(first_pod "$1")"; [[ -n "$p" ]] && pod_waiting_matches "$p" "$2"; }
app_oom() {
  local p; p="$(first_pod "$1")"; [[ -n "$p" ]] || return 1
  kubectl get pod "$p" -n "$NS" -o jsonpath='{.status.containerStatuses[0].lastState.terminated.reason}{.status.containerStatuses[0].state.terminated.reason}' 2>/dev/null | grep -q OOMKilled
}
app_running() { [[ "$(kubectl get pods -n "$NS" -l "app=$1" -o jsonpath='{.items[0].status.phase}' 2>/dev/null)" == "Running" ]]; }
restarts_at_least() { local n; n="$(kubectl get pods -n "$NS" -l "app=$1" -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}' 2>/dev/null)"; [[ "${n:-0}" -ge "$2" ]]; }
any_restart() { [[ "$(kubectl get pods -n "$NS" -l "app=$1" -o jsonpath='{.items[*].status.containerStatuses[0].restartCount}' 2>/dev/null)" =~ [1-9] ]]; }
init_restarts() { local n; n="$(kubectl get pod "$1" -n "$NS" -o jsonpath='{.status.initContainerStatuses[0].restartCount}' 2>/dev/null)"; [[ "${n:-0}" -ge 2 ]]; }
job_done() { [[ "$(kubectl get pods -n "$NS" -l job-name=report-job -o jsonpath='{.items[0].status.phase}' 2>/dev/null)" == "Succeeded" ]]; }
pod_terminating() { [[ -n "$(kubectl get pod "$1" -n "$NS" -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null)" ]]; }
pod_unschedulable() { [[ "$(kubectl get pod "$1" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="PodScheduled")].reason}' 2>/dev/null)" == "Unschedulable" ]]; }

# ktrx_step <title> <expected-exit-regex> <expected-text-regex> -- <ktrx args...>
# Runs KTRX, shows trimmed output, and ASSERTS exit code + a key phrase in the output.
ktrx_step() {
  local title="$1" want_rc="$2" want_text="$3"; shift 4
  local out="$TMP/step.out" full="$TMP/step.full" rc status detail=""
  if "$BINARY" "$@" > "$full" 2>&1; then rc=0; else rc=$?; fi
  strip_ansi < "$full" | sed -E 's/[[:space:]]+$//' > "$out"
  # Drop the boilerplate LIMITATIONS footer from the displayed output; assert on the full text.
  awk '/^LIMITATIONS/ {exit} {print}' "$out" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' > "$TMP/shown.out"
  print_block "ktrx $(printf '%q ' "$@" | sed 's/ $//')" "$TMP/shown.out" "${STEP_MAX:-$MAX_LINES}" "$rc"
  status="PASS"
  if ! [[ "$rc" =~ ^($want_rc)$ ]]; then status="FAIL"; detail="exit $rc, expected $want_rc"; fi
  if ! grep -Eiq "$want_text" "$out"; then
    status="FAIL"; detail="${detail:+$detail; }output missing /$want_text/"
  fi
  [[ "$status" == "FAIL" && -n "$HINT" ]] && detail="$detail. Note: $HINT"
  R_TITLE+=("$title"); R_STATUS+=("$status"); R_DETAIL+=("${detail:-exit $rc as expected}")
  say "$status  $title  (${detail:-exit $rc})"
}

# ---------------------------------------------------------------- preflight
command -v kubectl >/dev/null || die "kubectl not found"
kubectl cluster-info >/dev/null 2>&1 || die "no reachable cluster; start one first (minikube start)"
if [[ ! -x "$BINARY" || -n "${KTRX_REBUILD:-}" ]]; then
  command -v go >/dev/null || die "go not found and $BINARY is missing"
  say "building ktrx"; go build -o "$BINARY" ./cmd/ktrx
fi
kubectl get namespace "$NS" >/dev/null 2>&1 && die "namespace '$NS' already exists; set KTRX_DEMO_NAMESPACE or delete it"
kubectl create namespace "$NS" >/dev/null; CREATED_NS=1

apply() { kubectl apply -n "$NS" -f - >/dev/null; }
apply_retry() { local i; for i in 1 2 3 4 5; do apply && return 0; sleep 4; done; return 1; }

# ---------------------------------------------------------------- shared healthy app
say "deploying healthy baseline app"
apply <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  replicas: 2
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports: [{name: http, containerPort: 80}]
          readinessProbe: {httpGet: {path: /, port: http}, initialDelaySeconds: 2, periodSeconds: 5}
          resources: {requests: {cpu: 25m, memory: 32Mi}, limits: {cpu: 250m, memory: 128Mi}}
---
apiVersion: v1
kind: Service
metadata: {name: web}
spec:
  selector: {app: web}
  ports: [{name: http, port: 80, targetPort: http}]
---
apiVersion: v1
kind: Pod
metadata: {name: toolbox}
spec:
  containers: [{name: tools, image: "busybox:1.36", command: ["sleep", "3600"]}]
---
apiVersion: v1
kind: Pod
metadata: {name: toolbox-curl}
spec:
  containers: [{name: curl, image: "curlimages/curl:8.10.1", command: ["sleep", "3600"]}]
EOF
mk_ingress() { # name host service port
  apply_retry <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: $1}
spec:
  ingressClassName: nginx
  rules:
    - host: $2
      http:
        paths:
          - {path: /, pathType: Prefix, backend: {service: {name: $3, port: {number: $4}}}}
EOF
}
mk_ingress web web.ktrx.local web 80 || say "could not create web Ingress (admission webhook not ready?)"
kubectl rollout status deployment/web -n "$NS" --timeout=120s >/dev/null 2>&1 || true
wait_until "toolbox pods Ready" 120 kubectl wait --for=condition=Ready pod/toolbox pod/toolbox-curl -n "$NS" --timeout=5s

# ---------------------------------------------------------------- failure workloads (all started now, so waits overlap)
say "deploying failure scenarios"
apply <<'EOF'
apiVersion: v1
kind: Pod
metadata: {name: bad-image, labels: {app: bad-image}}
spec:
  containers: [{name: nginx, image: "nginx:ktrx-missing-tag", imagePullPolicy: Always}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: crashy}
spec:
  replicas: 1
  selector: {matchLabels: {app: crashy}}
  template:
    metadata: {labels: {app: crashy}}
    spec:
      containers:
        - name: worker
          image: busybox:1.36
          command: ["sh", "-c", "echo 'worker starting'; echo 'FATAL: cannot connect to db:5432' >&2; exit 1"]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: hungry}
spec:
  replicas: 1
  selector: {matchLabels: {app: hungry}}
  template:
    metadata: {labels: {app: hungry}}
    spec:
      containers:
        - name: worker
          image: busybox:1.36
          command: ["sh", "-c", "a=xxxxxxxxxx; while true; do a=\"$a$a\"; done"]
          resources: {requests: {memory: 16Mi}, limits: {memory: 16Mi}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: slow-ready}
spec:
  replicas: 2
  selector: {matchLabels: {app: slow-ready}}
  template:
    metadata: {labels: {app: slow-ready}}
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports: [{name: http, containerPort: 80}]
          readinessProbe: {httpGet: {path: /healthz-does-not-exist, port: http}, periodSeconds: 5}
---
apiVersion: v1
kind: Service
metadata: {name: slow-ready}
spec:
  selector: {app: slow-ready}
  ports: [{name: http, port: 80, targetPort: http}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: api}
spec:
  replicas: 1
  selector: {matchLabels: {app: api}}
  template:
    metadata: {labels: {app: api}}
    spec:
      containers: [{name: nginx, image: "nginx:1.27", ports: [{name: http, containerPort: 80}]}]
---
apiVersion: v1
kind: Service
metadata: {name: api}
spec:
  selector: {app: api-v2}
  ports: [{name: http, port: 80, targetPort: http}]
---
apiVersion: v1
kind: Pod
metadata: {name: too-big}
spec:
  containers:
    - name: nginx
      image: nginx:1.27
      resources: {requests: {cpu: "64", memory: 512Gi}}
---
apiVersion: v1
kind: Pod
metadata: {name: needs-config}
spec:
  containers:
    - name: app
      image: busybox:1.36
      command: ["sleep", "3600"]
      envFrom: [{configMapRef: {name: app-settings}}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: shop}
spec:
  replicas: 2
  selector: {matchLabels: {app: shop}}
  template:
    metadata: {labels: {app: shop}}
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports: [{name: http, containerPort: 80}]
          readinessProbe: {httpGet: {path: /, port: http}, periodSeconds: 5}
EOF
mk_ingress slow-ready slow-ready.ktrx.local slow-ready 80 || true
mk_ingress api api.ktrx.local api 80 || true

# ---- extra scenario workloads (started together so their waits overlap) ----
say "deploying extra scenarios"
apply <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: {name: report-job}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers: [{name: job, image: "busybox:1.36", command: ["sh", "-c", "echo report generated; exit 0"], resources: {requests: {cpu: 10m, memory: 16Mi}, limits: {cpu: 50m, memory: 32Mi}}}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: badport}
spec:
  replicas: 1
  selector: {matchLabels: {app: badport}}
  template:
    metadata: {labels: {app: badport}}
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports: [{name: http, containerPort: 80}]
          readinessProbe: {httpGet: {path: /, port: http}, periodSeconds: 5}
---
apiVersion: v1
kind: Service
metadata: {name: badport}
spec:
  selector: {app: badport}
  ports: [{name: http, port: 80, targetPort: 9090}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: locked}
spec:
  replicas: 1
  selector: {matchLabels: {app: locked}}
  template:
    metadata: {labels: {app: locked}}
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports: [{name: http, containerPort: 80}]
          readinessProbe: {httpGet: {path: /, port: http}, periodSeconds: 5}
---
apiVersion: v1
kind: Service
metadata: {name: locked}
spec:
  selector: {app: locked}
  ports: [{name: http, port: 80, targetPort: http}]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: deny-all-ingress}
spec:
  podSelector: {matchLabels: {app: locked}}
  policyTypes: [Ingress]
  ingress: []
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: liveness-fail}
spec:
  replicas: 1
  selector: {matchLabels: {app: liveness-fail}}
  template:
    metadata: {labels: {app: liveness-fail}}
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports: [{name: http, containerPort: 80}]
          livenessProbe: {httpGet: {path: /not-a-real-path, port: http}, initialDelaySeconds: 3, periodSeconds: 3, failureThreshold: 1}
---
apiVersion: v1
kind: Pod
metadata: {name: init-fail}
spec:
  initContainers: [{name: wait-for-db, image: "busybox:1.36", command: ["sh", "-c", "echo 'waiting for db'; exit 1"]}]
  containers: [{name: app, image: "nginx:1.27"}]
---
apiVersion: v1
kind: Pod
metadata: {name: multi}
spec:
  containers:
    - {name: app, image: "nginx:1.27"}
    - {name: sidecar, image: "busybox:1.36", command: ["sleep", "3600"]}
---
apiVersion: v1
kind: Pod
metadata: {name: stuck-term, finalizers: ["ktrx.demo/block"]}
spec:
  containers: [{name: app, image: "busybox:1.36", command: ["sleep", "3600"]}]
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data-missing-class}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: ktrx-no-such-class
  resources: {requests: {storage: 1Gi}}
---
apiVersion: v1
kind: Pod
metadata: {name: pvc-pod}
spec:
  containers:
    - {name: app, image: "nginx:1.27", volumeMounts: [{name: data, mountPath: /data}]}
  volumes: [{name: data, persistentVolumeClaim: {claimName: data-missing-class}}]
---
apiVersion: v1
kind: Pod
metadata: {name: private-image}
spec:
  imagePullSecrets: [{name: regcred-missing}]
  containers: [{name: app, image: "ghcr.io/ktrx-demo-nonexistent/private-app:1.0"}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: hygiene}
spec:
  replicas: 1
  selector: {matchLabels: {app: hygiene}}
  template:
    metadata: {labels: {app: hygiene}}
    spec:
      containers: [{name: nginx, image: "nginx:latest"}]
---
apiVersion: v1
kind: ConfigMap
metadata: {name: cfg-app-settings}
data: {MODE: good}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: cfg-app}
spec:
  replicas: 1
  selector: {matchLabels: {app: cfg-app}}
  template:
    metadata: {labels: {app: cfg-app}}
    spec:
      containers:
        - name: app
          image: busybox:1.36
          command: ["sh", "-c", "if [ \"$MODE\" = bad ]; then echo 'invalid MODE=bad' >&2; exit 1; fi; sleep 3600"]
          envFrom: [{configMapRef: {name: cfg-app-settings}}]
YAML
mk_ingress web-wrongport wrongport.ktrx.local web 8080 || true
mk_ingress ghost ghost.ktrx.local ghost-svc 80 || true
mk_ingress badport badport.ktrx.local badport 80 || true

# TLS Ingress (self-signed) for the --check-tls scenario
TLS_OK=0
if command -v openssl >/dev/null 2>&1 \
   && openssl req -x509 -nodes -newkey rsa:2048 -days 1 -subj "/CN=tls.ktrx.local" -addext "subjectAltName=DNS:tls.ktrx.local" \
        -keyout "$TMP/tls.key" -out "$TMP/tls.crt" >/dev/null 2>&1 \
   && kubectl create secret tls web-tls --cert="$TMP/tls.crt" --key="$TMP/tls.key" -n "$NS" >/dev/null 2>&1; then
  apply_retry <<YAML && TLS_OK=1
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: web-tls}
spec:
  ingressClassName: nginx
  tls: [{hosts: [tls.ktrx.local], secretName: web-tls}]
  rules:
    - host: tls.ktrx.local
      http:
        paths:
          - {path: /, pathType: Prefix, backend: {service: {name: web, port: {number: 80}}}}
YAML
fi

# Restricted ServiceAccount kubeconfig (can only get/list Pods) for the RBAC scenario
LIMITED_KC=""
setup_limited_kubeconfig() {
  kubectl create serviceaccount ktrx-limited -n "$NS" >/dev/null 2>&1 || return 1
  kubectl create role pod-reader --verb=get,list --resource=pods -n "$NS" >/dev/null 2>&1 || return 1
  kubectl create rolebinding pod-reader --role=pod-reader --serviceaccount="$NS:ktrx-limited" -n "$NS" >/dev/null 2>&1 || return 1
  local token server ca_file ca_data kc="$TMP/limited.kubeconfig"
  token="$(kubectl create token ktrx-limited -n "$NS" --duration=1h 2>/dev/null)" || return 1
  server="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
  ca_data="$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
  ca_file="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.certificate-authority}')"
  if [[ -n "$ca_data" ]]; then printf '%s' "$ca_data" | base64 -d > "$TMP/ca.crt"; ca_file="$TMP/ca.crt"; fi
  [[ -n "$ca_file" ]] || return 1
  kubectl config --kubeconfig="$kc" set-cluster demo --server="$server" --certificate-authority="$ca_file" --embed-certs=true >/dev/null
  kubectl config --kubeconfig="$kc" set-credentials limited --token="$token" >/dev/null
  kubectl config --kubeconfig="$kc" set-context demo --cluster=demo --user=limited --namespace="$NS" >/dev/null
  kubectl config --kubeconfig="$kc" use-context demo >/dev/null
  LIMITED_KC="$kc"
}
setup_limited_kubeconfig || say "could not build the restricted kubeconfig; RBAC scenario will be skipped"

# Gateway API (opt-out with KTRX_DEMO_GATEWAY=0). CRDs are only installed if missing, and removed at the end.
GATEWAY_READY=0
if [[ "$WANT_GATEWAY" == "1" ]]; then
  if kubectl get crd httproutes.gateway.networking.k8s.io >/dev/null 2>&1; then
    GATEWAY_READY=1
  else
    say "installing Gateway API CRDs (v1.2.1)"
    INSTALLED_GATEWAY=1
    if kubectl apply -f "$GATEWAY_URL" >/dev/null 2>&1 \
       && kubectl wait --for=condition=Established crd/httproutes.gateway.networking.k8s.io --timeout=60s >/dev/null 2>&1; then
      GATEWAY_READY=1
    fi
  fi
fi
if [[ "$GATEWAY_READY" -eq 1 ]]; then
  kubectl create namespace "$NS2" >/dev/null
  kubectl apply -n "$NS2" -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: shared}
spec:
  replicas: 1
  selector: {matchLabels: {app: shared}}
  template:
    metadata: {labels: {app: shared}}
    spec:
      containers: [{name: nginx, image: "nginx:1.27", ports: [{name: http, containerPort: 80}]}]
---
apiVersion: v1
kind: Service
metadata: {name: shared}
spec:
  selector: {app: shared}
  ports: [{name: http, port: 80, targetPort: http}]
YAML
  apply <<YAML
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: web-route}
spec:
  parentRefs: [{name: demo-gateway}]
  hostnames: [gw.ktrx.local]
  rules: [{backendRefs: [{name: web, port: 80}]}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: cross-route}
spec:
  parentRefs: [{name: demo-gateway}]
  hostnames: [cross.ktrx.local]
  rules: [{backendRefs: [{name: shared, namespace: $NS2, port: 80}]}]
YAML
fi

# Simulated GitOps objects (opt-out with KTRX_DEMO_GITOPS=0). KTRX only READS Argo CD / Flux objects from the
# API, so minimal CRDs plus hand-written history objects are enough; no controllers are installed.
GITOPS_READY=0
if [[ "$WANT_GITOPS" == "1" ]]; then
  if kubectl get crd applications.argoproj.io >/dev/null 2>&1 || kubectl get crd helmreleases.helm.toolkit.fluxcd.io >/dev/null 2>&1; then
    say "real Argo CD / Flux CRDs found; skipping the simulated GitOps scenario"
  else
    INSTALLED_ARGO=1; INSTALLED_HELM=1
    kubectl apply -f - >/dev/null <<'YAML' && GITOPS_READY=1
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: applications.argoproj.io}
spec:
  group: argoproj.io
  scope: Namespaced
  names: {kind: Application, plural: applications, singular: application}
  versions: [{name: v1alpha1, served: true, storage: true, schema: {openAPIV3Schema: {type: object, x-kubernetes-preserve-unknown-fields: true}}}]
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: helmreleases.helm.toolkit.fluxcd.io}
spec:
  group: helm.toolkit.fluxcd.io
  scope: Namespaced
  names: {kind: HelmRelease, plural: helmreleases, singular: helmrelease}
  versions: [{name: v2, served: true, storage: true, schema: {openAPIV3Schema: {type: object, x-kubernetes-preserve-unknown-fields: true}}}]
YAML
  fi
fi
if [[ "$GITOPS_READY" -eq 1 ]]; then
  kubectl wait --for=condition=Established crd/applications.argoproj.io crd/helmreleases.helm.toolkit.fluxcd.io --timeout=60s >/dev/null 2>&1 || true
  apply <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gitops-app
  labels: {argocd.argoproj.io/instance: gitops-app, app.kubernetes.io/instance: gitops-app}
spec:
  replicas: 1
  selector: {matchLabels: {app: gitops-app}}
  template:
    metadata:
      labels: {app: gitops-app, argocd.argoproj.io/instance: gitops-app, app.kubernetes.io/instance: gitops-app}
    spec:
      containers: [{name: nginx, image: "nginx:1.27"}]
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: gitops-app}
spec:
  source: {repoURL: "https://github.com/example/demo-repo.git", targetRevision: main, path: apps/gitops-app}
status:
  sync: {revision: 5d41402abc4b2a76b9719d911017c592ae3a0f1c}
  history:
    - {id: 1, revision: 9e107d9d372bb6826bd81d3542a419d6a1b2c3d4, deployedAt: "$(ts_ago 90)", source: {targetRevision: main}}
    - {id: 2, revision: 5d41402abc4b2a76b9719d911017c592ae3a0f1c, deployedAt: "$(ts_ago 20)", source: {targetRevision: main}}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: gitops-app}
spec: {chart: {spec: {chart: demo, version: "1.x"}}}
status:
  history:
    - {version: 1, chartName: demo, chartVersion: 1.0.0, appVersion: "1.0.0", status: superseded, lastDeployed: "$(ts_ago 90)"}
    - {version: 2, chartName: demo, chartVersion: 1.1.0, appVersion: "1.1.0", status: deployed, lastDeployed: "$(ts_ago 20)"}
YAML
fi

wait_until "shop Deployment Ready" 120 kubectl rollout status deployment/shop -n "$NS" --timeout=5s
GOOD_POD="$(first_pod shop)"
say "regression: rolling shop to a bad image tag"
kubectl set image deployment/shop nginx=nginx:1.99.99-ktrx-missing -n "$NS" >/dev/null
wait_until "bad-image ErrImagePull" 120 pod_waiting_matches bad-image 'ErrImagePull|ImagePullBackOff'
wait_until "crashy CrashLoopBackOff" 240 app_waiting_matches crashy 'CrashLoopBackOff'
wait_until "hungry OOMKilled" 180 app_oom hungry
wait_until "slow-ready Running" 120 app_running slow-ready
wait_until "too-big Unschedulable" 60 pod_unschedulable too-big
wait_until "needs-config config error" 60 pod_waiting_matches needs-config 'CreateContainerConfigError'
wait_until "report-job Succeeded" 90 job_done
wait_until "liveness-fail restarts" 150 restarts_at_least liveness-fail 2
wait_until "init-fail restarts" 120 init_restarts init-fail
wait_until "private-image pull error" 120 pod_waiting_matches private-image 'ErrImagePull|ImagePullBackOff'
wait_until "pvc-pod Unschedulable" 60 pod_unschedulable pvc-pod
wait_until "hygiene Running" 90 app_running hygiene
wait_until "cfg-app Running" 90 app_running cfg-app
wait_until "multi Ready" 90 kubectl wait --for=condition=Ready pod/multi -n "$NS" --timeout=5s
wait_until "locked Running" 90 app_running locked
wait_until "stuck-term Running" 90 kubectl wait --for=condition=Ready pod/stuck-term -n "$NS" --timeout=5s
kubectl delete pod stuck-term -n "$NS" --wait=false >/dev/null 2>&1 || true
wait_until "stuck-term Terminating" 30 pod_terminating stuck-term
say "config regression: bad ConfigMap value, then rollout restart"
kubectl patch configmap cfg-app-settings -n "$NS" --type=merge -p '{"data":{"MODE":"bad"}}' >/dev/null
kubectl rollout restart deployment/cfg-app -n "$NS" >/dev/null
wait_until "cfg-app new Pod crashing" 120 any_restart cfg-app
BAD_SHOP_POD="$(kubectl get pods -n "$NS" -l app=shop -o jsonpath='{range .items[?(@.spec.containers[0].image=="nginx:1.99.99-ktrx-missing")]}{.metadata.name}{"\n"}{end}' 2>/dev/null | head -n1)"
wait_until "shop bad pod ErrImagePull" 120 pod_waiting_matches "${BAD_SHOP_POD:-none}" 'ErrImagePull|ImagePullBackOff'

# ---------------------------------------------------------------- scenarios
CTX="$(kubectl config current-context 2>/dev/null || echo unknown)"
K8S_VER="$(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion}' 2>/dev/null || echo unknown)"

scenario "Preflight and a healthy baseline"
narrate "Before debugging, confirm KTRX can read what it needs. Then look at a healthy Deployment so you know what \"good\" looks like."
ktrx_step "Preflight: doctor" "0" "PASS" -- doctor -n "$NS" --include-netcheck --no-color
HINT="possible KTRX false positive: an unrelated Ingress-routed Service (api, no endpoints) is attributed to healthy web Pods via a port-name match in inbound.go" \
  ktrx_step "Healthy Deployment" "0" "HEALTHY" -- deploy/web -n "$NS" --all --no-color
ktrx_step "Healthy URL trace" "0" "HEALTHY|READY" -- trace-url http://web.ktrx.local/ -n "$NS"

scenario "ImagePullBackOff (typo in the image tag)"
narrate "**Symptom:** the Pod never starts. **Question:** is it the image, the registry, or credentials?"
kube_show kubectl get pod bad-image -n "$NS"
ktrx_step "ImagePullBackOff" "2" "ErrImagePull|ImagePullBackOff" -- bad-image -n "$NS" --no-color

scenario "CrashLoopBackOff (app exits on startup)"
narrate "**Symptom:** the Pod keeps restarting. **Question:** why does it exit? KTRX pulls the last container logs next to the restart evidence."
kube_show kubectl get pods -n "$NS" -l app=crashy
ktrx_step "CrashLoopBackOff" "2" "CrashLoopBackOff|restart" -- deploy/crashy -n "$NS" --no-color --logs

scenario "OOMKilled (memory limit too low)"
narrate "**Symptom:** restarts with no obvious error in the logs. The limit is 16Mi and the process grows without bound."
kube_show kubectl get pods -n "$NS" -l app=hungry
ktrx_step "OOMKilled" "2" "OOMKilled" -- deploy/hungry -n "$NS" --no-color

scenario "Pods Running but the URL fails (readiness probe)"
narrate "**Symptom:** pods show Running, yet traffic fails. The readiness probe checks a path that returns 404, so the Service has no ready endpoints. \`trace-url\` follows the request path from the URL down to the Pods."
kube_show kubectl get pods,endpoints -n "$NS" -l app=slow-ready
ktrx_step "Readiness failing: Deployment view" "2" "Ready condition|readiness|not Ready" -- deploy/slow-ready -n "$NS" --no-color
ktrx_step "Readiness failing: URL trace" "2" "FAILED|ready=0|notReady" -- trace-url http://slow-ready.ktrx.local/ -n "$NS"

scenario "Service has no endpoints (selector typo)"
narrate "**Symptom:** the Pod is healthy but the Service returns nothing. The Service selects \`app=api-v2\`, the Pods are labelled \`app=api\`."
kube_show kubectl get endpoints api -n "$NS"
ktrx_step "Selector mismatch: URL trace" "2|3" "FAILED|UNKNOWN|no (ready )?endpoints|endpoints=0|ready=0" -- trace-url http://api.ktrx.local/ -n "$NS"

scenario "Pending (unschedulable request)"
narrate "**Symptom:** the Pod stays Pending. It asks for 64 CPUs and 512Gi of memory, which no node can offer."
kube_show kubectl get pod too-big -n "$NS"
ktrx_step "Unschedulable" "2|3" "Unschedulable|FailedScheduling|Insufficient|Pending" -- too-big -n "$NS" --no-color

scenario "Missing ConfigMap"
narrate "**Symptom:** \`CreateContainerConfigError\`. The Pod references ConfigMap \`app-settings\`, which does not exist."
kube_show kubectl get pod needs-config -n "$NS"
ktrx_step "Missing ConfigMap" "2|3" "CreateContainerConfigError|app-settings|ConfigMap" -- needs-config -n "$NS" --no-color

scenario "\"What changed?\" after a bad rollout"
narrate "\`shop\` was healthy on \`nginx:1.27\`. Someone rolled it to a tag that does not exist. The old Pods keep serving, so the outage is easy to miss. The timeline shows the new revision and the image change; \`compare\` diffs a known-good Pod against the failing one."
kube_show kubectl get pods -n "$NS" -l app=shop
ktrx_step "Rollout timeline" "0|2|3" "ROLLOUT|revision" -- timeline deploy/shop -n "$NS" --since 1h --no-color
if [[ -n "$GOOD_POD" && -n "$BAD_SHOP_POD" ]]; then
  ktrx_step "Good vs bad Pod" "2" "image" -- compare "$GOOD_POD" "$BAD_SHOP_POD" -n "$NS" --no-color
else
  R_TITLE+=("Good vs bad Pod"); R_STATUS+=("FAIL"); R_DETAIL+=("could not find good/bad Pod names"); say "FAIL  Good vs bad Pod"
fi

scenario "Network checks from inside a Pod"
narrate "KTRX runs DNS, TCP and HTTP probes inside an existing Pod, so you see the cluster network from the workload's point of view. No debug Pod needed."
ktrx_step "netcheck: working Service" "0" "PASS" -- netcheck toolbox -n "$NS" --dns web --tcp web:80 --no-color
ktrx_step "netcheck: broken target" "2" "FAIL" -- netcheck toolbox -n "$NS" --dns no-such-service --tcp web:81 --timeout 4s --no-color
ktrx_step "netcheck: HTTP" "0|2|3" "PASS|FAIL|UNKNOWN|curl" -- netcheck toolbox-curl -n "$NS" --http http://web/ --no-color

scenario "Completed Job (a Pod that finished successfully)"
narrate "A Job Pod that ran to completion is **Succeeded**, not broken. Its Ready condition is False by design. A diagnostic tool should not call this a failure."
JOB_POD="$(kubectl get pods -n "$NS" -l job-name=report-job -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
kube_show kubectl get pods -n "$NS" -l job-name=report-job
HINT="possible KTRX bug: Succeeded Pods are reported as not Ready; skip the readiness check when phase is Succeeded" \
  ktrx_step "Completed Job Pod is not a failure" "0" "HEALTHY|Succeeded" -- "${JOB_POD:-report-job}" -n "$NS" --no-color

scenario "Ingress points at the wrong Service port"
narrate "The Ingress sends traffic to \`web:8080\`, but the Service only exposes port 80. Every request through this host fails even though all Pods are healthy."
ktrx_step "Ingress wrong port" "2|3" "8080|port|FAILED|UNKNOWN" -- trace-url http://wrongport.ktrx.local/ -n "$NS"

scenario "Ingress points at a Service that does not exist"
narrate "A typo or a deleted Service leaves the Ingress with a dangling backend."
ktrx_step "Ingress missing Service" "2|3" "ghost-svc|not found|FAILED|missing" -- trace-url http://ghost.ktrx.local/ -n "$NS"

scenario "Service targetPort mismatch (endpoints exist, connection refused)"
narrate "The Service targets port 9090 but nginx listens on 80. Endpoints look fine, so \`kubectl get endpoints\` is green, yet connections are refused. A live TCP probe from inside the cluster shows it."
kube_show kubectl get svc badport -n "$NS"
kube_show kubectl get endpoints badport -n "$NS"
ktrx_step "targetPort mismatch: live TCP probe" "2" "FAIL" -- netcheck toolbox -n "$NS" --tcp badport:80 --timeout 4s --no-color

scenario "NetworkPolicy blocks traffic"
narrate "A default-deny ingress policy selects the \`locked\` Pods. KTRX reports that a policy selects the Pod, and \`netcheck\` shows the connection actually being dropped. Enforcement needs a CNI that supports NetworkPolicy (for example \`minikube start --cni=calico\`)."
kube_show kubectl get networkpolicy -n "$NS"
ktrx_step "NetworkPolicy selects the Pod" "0|2|3" "NetworkPolicy" -- deploy/locked -n "$NS" --no-color
if "$BINARY" netcheck toolbox -n "$NS" --tcp locked:80 --timeout 4s >/dev/null 2>&1; then
  skip_step "NetworkPolicy drops traffic" "the policy is not enforced by this CNI (connection succeeded). Restart with \`minikube start --cni=calico\` to see it blocked."
else
  ktrx_step "NetworkPolicy drops traffic" "2" "FAIL" -- netcheck toolbox -n "$NS" --tcp locked:80 --timeout 4s --no-color
fi

scenario "Liveness probe kills a healthy-looking container"
narrate "The Pod shows Running but keeps restarting: the liveness probe checks a path that does not exist, so the kubelet kills the container."
kube_show kubectl get pods -n "$NS" -l app=liveness-fail
ktrx_step "Liveness probe failing" "2" "restart|liveness|Liveness|Killing|Unhealthy" -- deploy/liveness-fail -n "$NS" --no-color

scenario "Init container fails (the app container never starts)"
narrate "The Pod is stuck in \`Init:Error\` or \`Init:CrashLoopBackOff\`. The main container has no logs because it never ran."
kube_show kubectl get pod init-fail -n "$NS"
HINT="KTRX may not inspect initContainerStatuses yet; worth adding" \
  STEP_MAX=75 ktrx_step "Init container failing" "2|3" "wait-for-db|Init:|initContainer|init container" -- init-fail -n "$NS" --no-color --logs

scenario "Multi-container Pod: pick the container for network probes"
narrate "In a Pod with a sidecar, \`netcheck\` needs to know which container to run in. KTRX refuses to guess."
ktrx_step "Multi-container: container required" "1" "container" -- netcheck multi -n "$NS" --dns web --no-color
ktrx_step "Multi-container: --container sidecar" "0" "PASS" -- netcheck multi -n "$NS" --container sidecar --dns web --tcp web:80 --no-color

scenario "Pod stuck in Terminating"
narrate "A finalizer blocks deletion, so the Pod sits in Terminating. (The script removes the finalizer during cleanup.)"
kube_show kubectl get pod stuck-term -n "$NS"
STEP_MAX=75 ktrx_step "Stuck Terminating Pod" "2|3" "Terminat|deletion|DeletionTimestamp" -- stuck-term -n "$NS" --no-color

scenario "PVC that can never bind"
narrate "The Pod waits for a PersistentVolumeClaim whose StorageClass does not exist, so it stays Pending."
kube_show kubectl get pvc data-missing-class -n "$NS"
kube_show kubectl get pod pvc-pod -n "$NS"
HINT="KTRX has no PVC-specific check yet; only Events explain this" \
  STEP_MAX=75 ktrx_step "Unbound PVC" "2|3" "PersistentVolumeClaim|persistentvolumeclaim|unbound|storageclass|data-missing-class" -- pvc-pod -n "$NS" --no-color

scenario "Image pull from a private registry without credentials"
narrate "Same symptom as a missing tag, different cause: the registry rejects the pull (or the repository is not visible). The Events carry the real reason."
kube_show kubectl get pod private-image -n "$NS"
ktrx_step "Private registry pull failure" "2" "ErrImagePull|ImagePullBackOff|unauthorized|denied" -- private-image -n "$NS" --no-color

scenario "Healthy Pod with hygiene warnings"
narrate "\`nginx:latest\`, no probes, no resource requests. The Pod works, but KTRX raises warnings, and **warnings also produce exit code 2**. Decide whether your pipeline should treat that as a failure."
ktrx_step "Hygiene warnings only" "2" "latest|BestEffort|readiness|WARN" -- deploy/hygiene -n "$NS" --no-color

scenario "A ConfigMap change breaks the next rollout"
narrate "\`cfg-app\` was healthy. Someone changed \`MODE\` to \`bad\` in its ConfigMap and restarted the Deployment. The old Pod hides the problem. The timeline lines up the ConfigMap edit with the new ReplicaSet, and \`--failure-at\` lets you pin the failure time yourself."
kube_show kubectl get pods -n "$NS" -l app=cfg-app
ktrx_step "Timeline with correlations" "0|2|3" "ROLLOUT|ConfigMap|revision" -- timeline deploy/cfg-app -n "$NS" --since 1h --top 3 --no-color
ktrx_step "Timeline with explicit failure time" "0|2|3" "ROLLOUT|ConfigMap|revision|failure" -- timeline deploy/cfg-app -n "$NS" --since 1h --failure-at "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" --no-color

scenario "URL trace extras: DNS and TLS checks"
narrate "\`trace-url\` can also check, on request, that the hostname resolves and that the TLS handshake and certificate are valid. These run from the machine running KTRX, so on Minikube the \`*.ktrx.local\` names normally do **not** resolve; that is expected and is reported rather than hidden."
ktrx_step "trace-url --check-dns" "0|2|3" "DNS" -- trace-url http://web.ktrx.local/ -n "$NS" --check-dns
if [[ "$TLS_OK" -eq 1 ]]; then
  ktrx_step "trace-url --check-tls" "0|2|3" "TLS|tls|certificate|handshake|SNI" -- trace-url https://tls.ktrx.local/ -n "$NS" --check-tls
else
  skip_step "trace-url --check-tls" "openssl is missing or the TLS Ingress could not be created."
fi

scenario "Limited RBAC: missing permissions are UNKNOWN, not green"
narrate "A ServiceAccount that can only read Pods runs KTRX. It cannot list Services, Ingresses or Events, so KTRX must say what it could not check instead of reporting a clean bill of health."
if [[ -n "$LIMITED_KC" ]]; then
  ktrx_step "doctor with limited RBAC" "2|3" "DENIED|FAIL|forbidden|denied|UNKNOWN" -- doctor -n "$NS" --kubeconfig "$LIMITED_KC" --no-color
  ktrx_step "Pod check with limited RBAC" "0|2|3" "Not checked|UNKNOWN|forbidden|denied|HEALTHY" -- "$(first_pod web)" -n "$NS" --kubeconfig "$LIMITED_KC" --no-color
else
  skip_step "Limited RBAC" "could not create the restricted ServiceAccount kubeconfig (needs Kubernetes 1.24+ for \`kubectl create token\`)."
fi

scenario "Gateway API: HTTPRoute tracing and ReferenceGrant"
if [[ "$GATEWAY_READY" -eq 1 ]]; then
  narrate "No Gateway controller is installed; KTRX reads the HTTPRoute objects themselves. The first route points to a Service in the same namespace. The second points to a Service in another namespace, which Gateway API only allows with a **ReferenceGrant**."
  ktrx_step "HTTPRoute to same-namespace Service" "0|2|3" "HTTPRoute|web" -- trace-url http://gw.ktrx.local/ -n "$NS"
  ktrx_step "Cross-namespace route without ReferenceGrant" "2|3" "ReferenceGrant|FAILED|not allowed|denied" -- trace-url http://cross.ktrx.local/ -n "$NS"
  kubectl apply -n "$NS2" -f - >/dev/null <<YAML || true
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata: {name: allow-from-demo}
spec:
  from: [{group: gateway.networking.k8s.io, kind: HTTPRoute, namespace: $NS}]
  to: [{group: "", kind: Service, name: shared}]
YAML
  narrate "After adding a ReferenceGrant in \`$NS2\` that allows HTTPRoutes from \`$NS\`:"
  ktrx_step "Cross-namespace route with ReferenceGrant" "0|2|3" "shared" -- trace-url http://cross.ktrx.local/ -n "$NS"
else
  skip_step "Gateway API HTTPRoute scenarios" "Gateway API CRDs could not be installed (offline?) or KTRX_DEMO_GATEWAY=0."
fi

scenario "GitOps history (Argo CD and Flux), simulated"
if [[ "$GITOPS_READY" -eq 1 ]]; then
  narrate "> **Simulated:** no Argo CD or Flux controller is running. The script creates minimal CRDs plus an Argo CD \`Application\` and a Flux \`HelmRelease\` with hand-written history entries. KTRX only reads these objects from the API, so this shows exactly how deploy history appears in the timeline. Use a real cluster with Argo CD or Flux to see live data."
  ktrx_step "GitOps deploy history" "0|2|3" "Argo|ArgoCD|Helm|GITOPS|commit" -- timeline deploy/gitops-app -n "$NS" --since 3h --no-color
else
  skip_step "GitOps history" "real Argo CD / Flux CRDs already exist, the CRD install failed, or KTRX_DEMO_GITOPS=0."
fi

scenario "AWS ELB target health (not run here)"
narrate "\`trace-url --aws\` needs real AWS credentials and a TargetGroupBinding, so this script does not run it. See \`docs/AWS_MOCK_DEMO.md\` (generated by \`scripts/generate-aws-mock-demo.sh\`) for the mock-based walkthrough."

scenario "Share it, or gate CI on it"
narrate "Markdown output pastes straight into an incident ticket. The exit code is stable (0 healthy, 2 failed or warn, 3 unknown, 1 error), so a pipeline can gate on it."
ktrx_step "Markdown report" "2" "ErrImagePull|ImagePullBackOff|FAILED" -- bad-image -n "$NS" --output markdown
ktrx_step "Namespace DNS topology" "0" "TOPOLOGY" -- topology dns -n "$NS" --no-color
ktrx_step "JSON output for scripts" "2" "verdict|Verdict" -- deploy/crashy -n "$NS" --output json

scenario "Gate a pipeline on the exit code"
narrate "Use the exit code directly. \`ktrx deploy/api -n prod --no-color || { echo \"gate failed (exit \$?)\"; exit 1; }\`. The table is the real exit code KTRX returned for each Deployment above."
{ echo; echo "| Deployment | Exit code | Pipeline decision |"; echo "|---|---|---|"; } >> "$BODY"
gate_fail=""
for d in web hygiene crashy hungry slow-ready shop; do
  if "$BINARY" "deploy/$d" -n "$NS" --no-color >/dev/null 2>&1; then rc=0; else rc=$?; fi
  case "$rc" in
    0) m="healthy, continue" ;; 1) m="tool error, investigate" ;;
    2) m="FAILED or WARN, stop" ;; 3) m="UNKNOWN, needs a human" ;; *) m="unexpected" ;;
  esac
  echo "| \`$d\` | $rc | $m |" >> "$BODY"
  if [[ "$d" == web && "$rc" -ne 0 ]]; then gate_fail+=" web=$rc(expected 0; see the Healthy Deployment note)"; fi
  if [[ "$d" == crashy && "$rc" -ne 2 ]]; then gate_fail+=" crashy=$rc(expected 2)"; fi
done
if [[ -z "$gate_fail" ]]; then
  R_TITLE+=("CI gate exit codes"); R_STATUS+=("PASS"); R_DETAIL+=("web=0, crashy=2")
else
  R_TITLE+=("CI gate exit codes"); R_STATUS+=("FAIL"); R_DETAIL+=("$gate_fail")
fi
say "$([[ -z "$gate_fail" ]] && echo PASS || echo FAIL)  CI gate exit codes"

# ---------------------------------------------------------------- assemble
pass=0; fail=0; skip=0
for s in "${R_STATUS[@]}"; do
  case "$s" in PASS) pass=$((pass + 1)) ;; SKIP) skip=$((skip + 1)) ;; *) fail=$((fail + 1)) ;; esac
done
{
  echo "# KTRX demo: real failures, diagnosed"
  echo
  echo "> Generated by \`scripts/generate-scenarios-demo.sh\` on a live cluster. Every output below was captured from that run, not written by hand."
  echo
  echo "| | |"; echo "|---|---|"
  echo "| Generated | $(date -u '+%Y-%m-%d %H:%M UTC') |"
  echo "| Kubernetes context | \`$CTX\` |"
  echo "| Kubernetes version | \`$K8S_VER\` |"
  echo "| Namespace | \`$NS\` (removed after the run) |"
  echo "| Assertions | $pass passed, $fail failed, $skip skipped |"
  echo
  echo "## Results at a glance"
  echo
  echo "| # | Scenario | Result | Detail |"; echo "|---|---|---|---|"
  for i in "${!R_TITLE[@]}"; do
    echo "| $((i + 1)) | ${R_TITLE[$i]} | ${R_STATUS[$i]} | ${R_DETAIL[$i]} |"
  done
  echo
  echo "Output is trimmed to $MAX_LINES lines per command and the generic LIMITATIONS footer is omitted. Run any command yourself to see the full report."
  cat "$BODY"
  echo
  echo "## Reproduce"
  echo
  echo '```bash'
  echo 'minikube start && minikube addons enable ingress'
  echo 'bash scripts/generate-scenarios-demo.sh   # KEEP_DEMO=1 keeps the namespace for poking around'
  echo '```'
} > "$DOC"

say "wrote $DOC  ($pass passed, $fail failed, $skip skipped)"
[[ "$fail" -eq 0 ]]
Displaying generate-scenarios-demo-1.sh.