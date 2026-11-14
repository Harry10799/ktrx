package cli

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildTopologyReportNamespaceScopeAndDNSReferences(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "shop", Labels: map[string]string{"app": "api"}},
		Spec: corev1.PodSpec{DNSPolicy: corev1.DNSClusterFirst, Containers: []corev1.Container{{
			Name: "api",
			Env:  []corev1.EnvVar{{Name: "ORDERS_URL", Value: "http://orders.shop.svc.cluster.local:8080"}},
			Args: []string{"--metrics=https://metrics.example.com/health"},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.244.0.8", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	services := []corev1.Service{
		{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.10", Selector: map[string]string{"app": "api"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.20", Selector: map[string]string{"app": "orders"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "empty-selector", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.30"}},
	}

	report := buildTopologyReport("shop", []corev1.Pod{pod}, services)
	if report.Namespace != "shop" {
		t.Fatalf("namespace = %q, want shop", report.Namespace)
	}
	if !hasTopologyEdge(report.Edges, "service/api", "pod/api-abc", "selects") {
		t.Fatal("expected Service selector relationship")
	}
	if !hasTopologyEdge(report.Edges, "pod/api-abc", "dns/orders.shop.svc.cluster.local", "references") {
		t.Fatal("expected declared internal DNS reference")
	}
	if !hasTopologyEdge(report.Edges, "dns/orders.shop.svc.cluster.local", "service/orders", "matches Service DNS name") {
		t.Fatal("expected DNS name to match the namespace Service")
	}
	if !hasTopologyEdge(report.Edges, "pod/api-abc", "dns/metrics.example.com", "references") {
		t.Fatal("expected external DNS reference")
	}
	if hasTopologyEdge(report.Edges, "service/empty-selector", "pod/api-abc", "selects") {
		t.Fatal("empty Service selector must not match every Pod")
	}
	for _, node := range report.Nodes {
		if strings.Contains(node.ID, "default/") {
			t.Fatalf("unexpected cross-namespace node: %s", node.ID)
		}
	}
}

func hasTopologyEdge(edges []topologyEdge, from, to, relation string) bool {
	for _, edge := range edges {
		if edge.From == from && edge.To == to && edge.Relation == relation {
			return true
		}
	}
	return false
}
