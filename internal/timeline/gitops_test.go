package timeline

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestCommitHashExtractsFluxRevision(t *testing.T) {
	tests := []struct {
		revision string
		want     string
	}{
		{revision: "main@sha1:0123456789abcdef", want: "0123456789abcdef"},
		{revision: "release@sha256:abcdef012345", want: "abcdef012345"},
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
		"metadata": map[string]interface{}{"name": "apps", "namespace": "production"},
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
		"metadata": map[string]interface{}{"name": "platform", "namespace": "production"},
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
