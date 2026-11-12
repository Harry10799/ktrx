package cli

import (
	"testing"
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestURLHostMatches(t *testing.T) {
	tests := []struct{ pattern, host string; want bool }{
		{"api.example.com", "api.example.com", true},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "a.b.example.com", false},
		{"*.example.com", "example.com", false},
		{"*", "anything.example", true},
	}
	for _, tt := range tests {
		if got := urlHostMatches(tt.pattern, tt.host); got != tt.want {
			t.Errorf("urlHostMatches(%q, %q) = %t, want %t", tt.pattern, tt.host, got, tt.want)
		}
	}
}

func TestURLPathMatches(t *testing.T) {
	tests := []struct{ rule, request, kind string; want bool }{
		{"/", "/anything", "Prefix", true},
		{"/api", "/api/v1", "Prefix", true},
		{"/api", "/apix", "Prefix", false},
		{"/api", "/api", "Exact", true},
		{"/api", "/api/v1", "Exact", false},
		{"/api", "/api/v1", "PathPrefix", true},
	}
	for _, tt := range tests {
		if got := urlPathMatches(tt.rule, tt.request, tt.kind); got != tt.want {
			t.Errorf("urlPathMatches(%q, %q, %q) = %t, want %t", tt.rule, tt.request, tt.kind, got, tt.want)
		}
	}
}

func TestURLGlobMatchALBPaths(t *testing.T) {
	tests := []struct { pattern, request string; want bool }{
		{"/api/*", "/api/v1/users", true},
		{"/*", "/health/ready", true},
		{"/api/?", "/api/x", true},
		{"/api/*", "/apix", false},
	}
	for _, tt := range tests {
		got, err := urlGlobMatch(tt.pattern, tt.request)
		if err != nil { t.Fatalf("urlGlobMatch returned error: %v", err) }
		if got != tt.want { t.Errorf("urlGlobMatch(%q, %q) = %t, want %t", tt.pattern, tt.request, got, tt.want) }
	}
}

func TestURLRoutePrecedence(t *testing.T) {
	exactHost := urlTraceRoute{Host:"api.example.com", Path:"/api", Match:"Prefix"}
	wildcardHost := urlTraceRoute{Host:"*.example.com", Path:"/api/v1", Match:"Exact"}
	longerPrefix := urlTraceRoute{Host:"api.example.com", Path:"/api/v1", Match:"Prefix"}
	exactPath := urlTraceRoute{Host:"api.example.com", Path:"/api", Match:"Exact"}
	if urlRoutePrecedence(exactHost) <= urlRoutePrecedence(wildcardHost) { t.Fatal("exact host should outrank wildcard host") }
	if urlRoutePrecedence(longerPrefix) <= urlRoutePrecedence(exactPath) { t.Fatal("longer path should outrank shorter path before match-type tie-break") }
}

func TestResolveURLBackendIncludesNotReadyEndpointPods(t *testing.T) {
	ready, notReady := true, false
	client := fake.NewSimpleClientset(
		&corev1.Service{ObjectMeta:metav1.ObjectMeta{Name:"api", Namespace:"prod"}, Spec:corev1.ServiceSpec{Ports:[]corev1.ServicePort{{Name:"http", Port:80}}}},
		&discoveryv1.EndpointSlice{
			ObjectMeta:metav1.ObjectMeta{Name:"api-1", Namespace:"prod", Labels:map[string]string{"kubernetes.io/service-name":"api"}},
			Endpoints:[]discoveryv1.Endpoint{
				{Addresses:[]string{"10.0.0.1"}, Conditions:discoveryv1.EndpointConditions{Ready:&ready}, TargetRef:&corev1.ObjectReference{Kind:"Pod", Namespace:"prod", Name:"api-ready"}},
				{Addresses:[]string{"10.0.0.2"}, Conditions:discoveryv1.EndpointConditions{Ready:&notReady}, TargetRef:&corev1.ObjectReference{Kind:"Pod", Namespace:"prod", Name:"api-unready"}},
			},
		},
		&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"api-ready", Namespace:"prod", UID:types.UID("p1")}, Status:corev1.PodStatus{Phase:corev1.PodRunning, Conditions:[]corev1.PodCondition{{Type:corev1.PodReady, Status:corev1.ConditionTrue}}}},
		&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"api-unready", Namespace:"prod", UID:types.UID("p2")}, Status:corev1.PodStatus{Phase:corev1.PodRunning, Conditions:[]corev1.PodCondition{{Type:corev1.PodReady, Status:corev1.ConditionFalse}}}},
	)
	backend := urlTraceBackend{Namespace:"prod", Service:"api", Port:"http"}
	resolveURLBackend(context.Background(), client, "prod", &backend)
	if backend.EndpointCount != 2 || backend.ReadyEndpoints != 1 {
		t.Fatalf("unexpected endpoint counts: total=%d ready=%d", backend.EndpointCount, backend.ReadyEndpoints)
	}
	if len(backend.Pods) != 2 { t.Fatalf("expected ready and not-ready Pods, got %+v", backend.Pods) }
	foundNotReady := false
	for _, pod := range backend.Pods {
		if pod.Name == "api-unready" && !pod.EndpointReady && !pod.Ready { foundNotReady = true }
	}
	if !foundNotReady { t.Fatalf("not-ready endpoint Pod was not surfaced: %+v", backend.Pods) }
}
