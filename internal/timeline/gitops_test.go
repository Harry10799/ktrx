package timeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestCommitHashExtractsFluxRevision(t *testing.T) {
	tests := []struct {
		revision string
		want     string
	}{
		{revision: "main@sha1:0123456789abcdef0123456789abcdef01234567", want: "0123456789abcdef0123456789abcdef01234567"},
		{revision: "release@sha256:abcdef012345", want: ""},
		{revision: "main@sha1:not-a-valid-commit", want: ""},
		{revision: "main@branch", want: ""},
	}
	for _, test := range tests {
		if got := commitHash(test.revision); got != test.want {
			t.Errorf("commitHash(%q) = %q, want %q", test.revision, got, test.want)
		}
	}
}

func TestFluxInventoryIDUsesCanonicalNamespaceNameGroupKindFormat(t *testing.T) {
	if !fluxInventoryMatchesDeployment("production_orders_apps_Deployment", "production", "orders") {
		t.Fatal("expected canonical Flux inventory ID to match Deployment")
	}
	if fluxInventoryMatchesDeployment("staging_orders_apps_Deployment", "production", "orders") {
		t.Fatal("inventory ID from another namespace must not match")
	}
}

func TestAddGitOpsEventsReadsKustomizationAndGitCommit(t *testing.T) {
	revisionTime := "2026-11-12T09:30:00Z"
	kustomization := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "apps", "namespace": "flux-system"},
		"spec": map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "platform"}},
		"status": map[string]interface{}{
			"lastAppliedRevision": "main@sha1:0123456789abcdef0123456789abcdef01234567",
			"conditions": []interface{}{map[string]interface{}{"lastTransitionTime": revisionTime}},
			"inventory": map[string]interface{}{"entries": []interface{}{map[string]interface{}{"id": "production_orders_apps_Deployment"}}},
		},
	}}
	gitRepository := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind": "GitRepository",
		"metadata": map[string]interface{}{"name": "platform", "namespace": "flux-system"},
		"spec": map[string]interface{}{"url": "https://github.com/example/platform.git"},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), kustomization, gitRepository)
	report := &Report{Namespace: "production", Target: "Deployment/orders"}
	addGitOpsEvents(context.Background(), client, report, map[string]string{})
	if len(report.Events) != 1 {
		t.Fatalf("got %d GitOps events, want 1: %+v", len(report.Events), report.Events)
	}
	details := report.Events[0].Details
	if !strings.Contains(details, "commit=0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("commit hash missing from GitOps event: %s", details)
	}
	if !strings.Contains(details, "https://github.com/example/platform/commit/0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("commit URL missing from GitOps event: %s", details)
	}
}

func TestArgoCDHistoryMatchesDeploymentAndResolvesCommit(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind": "Application",
		"metadata": map[string]interface{}{"name": "orders", "namespace": "argocd"},
		"spec": map[string]interface{}{"source": map[string]interface{}{"repoURL": "https://github.com/example/orders.git"}},
		"status": map[string]interface{}{
			"resources": []interface{}{map[string]interface{}{"group": "apps", "kind": "Deployment", "namespace": "production", "name": "orders"}},
			"history": []interface{}{map[string]interface{}{
				"id": int64(3), "revision": "0123456789abcdef0123456789abcdef01234567",
				"deployedAt": "2026-11-12T09:45:00Z",
			}},
		},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), app)
	report := &Report{Namespace: "production", Target: "Deployment/orders"}
	addArgoCDHistory(context.Background(), client, report, map[string]string{})
	if len(report.Events) != 1 {
		t.Fatalf("got %d Argo events, want 1: %+v", len(report.Events), report.Events)
	}
	event := report.Events[0]
	if event.Source != "argocd-application-history" || !strings.Contains(event.Details, "commitURL=https://github.com/example/orders/commit/0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("Argo commit history not resolved: %+v", event)
	}
}

func TestFluxKustomizationTimestampDoesNotReuseStaleReadyTransition(t *testing.T) {
	object := unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"generation": int64(4)},
		"status": map[string]interface{}{
			"lastAppliedRevision": "main@sha1:0123456789abcdef0123456789abcdef01234567",
			"lastAttemptedRevision": "main@sha1:0123456789abcdef0123456789abcdef01234567",
			"conditions": []interface{}{map[string]interface{}{"type":"Ready", "status":"True", "lastTransitionTime":"2026-01-01T00:00:00Z"}},
		},
	}}
	if got := fluxKustomizationTime(object); got != nil {
		t.Fatalf("stale Ready transition must not be used as reconcile time: %v", got)
	}
	object.Object["status"].(map[string]interface{})["lastHandledReconcileAt"] = "2026-11-12T09:30:00Z"
	if got := fluxKustomizationTime(object); got != nil {
		t.Fatalf("lastHandledReconcileAt echoes requestedAt and must not be treated as apply time: %v", got)
	}
}

func TestFluxKustomizationEventProvidesAppliedRevisionAndTimestamp(t *testing.T) {
 event := &unstructured.Unstructured{Object:map[string]interface{}{
  "apiVersion":"v1", "kind":"Event",
  "metadata":map[string]interface{}{"name":"apps.123", "namespace":"flux-system", "creationTimestamp":"2026-11-12T09:31:00Z"},
  "involvedObject":map[string]interface{}{"kind":"Kustomization", "name":"apps", "namespace":"flux-system"},
  "reason":"ReconciliationSucceeded", "message":"Kustomization applied revision: main@sha1:0123456789abcdef0123456789abcdef01234567",
 }}
 client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), event)
 object := unstructured.Unstructured{Object:map[string]interface{}{"metadata":map[string]interface{}{"name":"apps", "namespace":"flux-system"}}}
 timestamp, revision, found := fluxKustomizationEvent(context.Background(), client, object)
 if !found || timestamp == nil { t.Fatalf("expected successful Flux Event timestamp, found=%t timestamp=%v", found, timestamp) }
 if timestamp.UTC().Format(time.RFC3339) != "2026-11-12T09:31:00Z" { t.Fatalf("unexpected Event timestamp: %v", timestamp) }
 if revision != "main@sha1:0123456789abcdef0123456789abcdef01234567" { t.Fatalf("unexpected applied revision: %q", revision) }
}

func TestBitbucketCommitURL(t *testing.T) {
	got := gitRepositoryCommitURL("https://bitbucket.org/example/platform.git", "0123456789abcdef0123456789abcdef01234567")
	want := "https://bitbucket.org/example/platform/commits/0123456789abcdef0123456789abcdef01234567"
	if got != want { t.Fatalf("got %q, want %q", got, want) }
}
