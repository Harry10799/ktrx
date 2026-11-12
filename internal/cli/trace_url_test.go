package cli

import (
	"testing"
	"context"
	"crypto/x509"
	"net"
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		{"/api/*", "/api/v1/users", "ImplementationSpecific", true},
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
				{Addresses:[]string{"10.0.0.3"}, Conditions:discoveryv1.EndpointConditions{}, TargetRef:&corev1.ObjectReference{Kind:"Pod", Namespace:"prod", Name:"api-unknown"}},
			},
		},
		&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"api-ready", Namespace:"prod", UID:types.UID("p1")}, Status:corev1.PodStatus{Phase:corev1.PodRunning, Conditions:[]corev1.PodCondition{{Type:corev1.PodReady, Status:corev1.ConditionTrue}}}},
		&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"api-unready", Namespace:"prod", UID:types.UID("p2")}, Status:corev1.PodStatus{Phase:corev1.PodRunning, Conditions:[]corev1.PodCondition{{Type:corev1.PodReady, Status:corev1.ConditionFalse}}}},
		&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"api-unknown", Namespace:"prod", UID:types.UID("p3")}, Status:corev1.PodStatus{Phase:corev1.PodRunning, Conditions:[]corev1.PodCondition{{Type:corev1.PodReady, Status:corev1.ConditionTrue}}}},
	)
	backend := urlTraceBackend{Namespace:"prod", Service:"api", Port:"http"}
	resolveURLBackend(context.Background(), client, "prod", &backend)
	if backend.EndpointCount != 3 || backend.ReadyEndpoints != 2 {
		t.Fatalf("unexpected endpoint counts: total=%d ready=%d", backend.EndpointCount, backend.ReadyEndpoints)
	}
	if len(backend.Pods) != 3 { t.Fatalf("expected three endpoint Pods, got %+v", backend.Pods) }
	foundNotReady := false
	for _, pod := range backend.Pods {
		if pod.Name == "api-unready" && !pod.EndpointReady && !pod.Ready { foundNotReady = true }
	}
	if !foundNotReady { t.Fatalf("not-ready endpoint Pod was not surfaced: %+v", backend.Pods) }
}

func TestDNSAliasMatchesALBByResolvedIP(t *testing.T) {
 dns := &urlTraceDNS{Resolved: []string{"203.0.113.10", "203.0.113.11"}}
 routes := []urlTraceRoute{{LoadBalancerHosts: []string{"203.0.113.11", "203.0.113.12"}}}
 matched, method, _ := dnsMatchesALB(context.Background(), dns, routes)
 if !matched || method != "IP" { t.Fatalf("expected Route 53 alias IP match, got matched=%t method=%q", matched, method) }
}

func TestDNSMismatchExplainsFrontEnd(t *testing.T) {
 dns := &urlTraceDNS{Resolved: []string{"198.51.100.20"}}
 routes := []urlTraceRoute{{LoadBalancerHosts: []string{"203.0.113.20"}}}
 matched, _, pointsTo := dnsMatchesALB(context.Background(), dns, routes)
 if matched || !strings.Contains(pointsTo, "CloudFront or Global Accelerator") { t.Fatalf("expected explained mismatch, got matched=%t target=%q", matched, pointsTo) }
}

func TestDNSJSONPreservesFalseAndUnchecked(t *testing.T) {
 data, err := json.Marshal(urlTraceDNS{MatchesALB:false, Checked:false})
 if err != nil { t.Fatal(err) }
 if !strings.Contains(string(data), "\"matchesALB\":false") || !strings.Contains(string(data), "\"checked\":false") { t.Fatalf("false state omitted: %s", data) }
}

func TestExternalCheckVerdict(t *testing.T) {
 report := urlTraceReport{Verdict:"HEALTHY", DNS:&urlTraceDNS{Resolved:[]string{"203.0.113.1"}, Checked:true, Status:"HEALTHY", MatchMethod:"IP", MatchesALB:false}}
 applyURLExternalCheckVerdict(&report)
 if report.Verdict != "WARN" { t.Fatalf("DNS mismatch should warn, got %s", report.Verdict) }
 report = urlTraceReport{Verdict:"HEALTHY", TLS:&urlTraceTLS{Status:"FAILED", Error:"certificate expired"}}
 applyURLExternalCheckVerdict(&report)
 if report.Verdict != "FAILED" { t.Fatalf("TLS error should fail, got %s", report.Verdict) }
}

func TestExternalReachabilityErrorsAreConservative(t *testing.T) {
	timeoutErr := &net.DNSError{Err:"i/o timeout", IsTimeout:true}
	nxdomainErr := &net.DNSError{Err:"no such host", IsNotFound:true}
	if isURLDNSNotFound(timeoutErr) { t.Fatal("DNS timeout must not be classified as NXDOMAIN") }
	if !isURLDNSNotFound(nxdomainErr) { t.Fatal("NXDOMAIN should be classified as a definitive DNS failure") }

	report := urlTraceReport{Verdict:"HEALTHY", DNS:&urlTraceDNS{Status:"UNKNOWN", Error:timeoutErr.Error()}}
	applyURLExternalCheckVerdict(&report)
	if report.Verdict != "HEALTHY" { t.Fatalf("local DNS timeout must not fail cluster verdict, got %s", report.Verdict) }
	report = urlTraceReport{Verdict:"HEALTHY", DNS:&urlTraceDNS{Status:"FAILED", Error:nxdomainErr.Error()}}
	applyURLExternalCheckVerdict(&report)
	if report.Verdict != "FAILED" { t.Fatalf("NXDOMAIN should fail, got %s", report.Verdict) }

	report = urlTraceReport{Verdict:"HEALTHY", TLS:&urlTraceTLS{Status:"UNKNOWN", Error:"i/o timeout"}}
	applyURLExternalCheckVerdict(&report)
	if report.Verdict != "HEALTHY" { t.Fatalf("TLS timeout must not fail cluster verdict, got %s", report.Verdict) }
}

func TestExpiredCertificateIsRecognizedAsCertificateFailure(t *testing.T) {
	err := x509.CertificateInvalidError{Reason:x509.Expired}
	if !isURLCertificateError(err) { t.Fatal("x509 expired certificate error should be definitive") }
	report := urlTraceReport{Verdict:"HEALTHY", TLS:&urlTraceTLS{Status:"FAILED", Error:"x509: certificate has expired"}}
	applyURLExternalCheckVerdict(&report)
	if report.Verdict != "FAILED" { t.Fatalf("expired certificate should fail, got %s", report.Verdict) }
}

func TestTargetGroupBindingMatchesNamedServicePort(t *testing.T) {
 gvr := schema.GroupVersionResource{Group:"elbv2.k8s.aws", Version:"v1beta1", Resource:"targetgroupbindings"}
 binding := &unstructured.Unstructured{Object:map[string]interface{}{"apiVersion":"elbv2.k8s.aws/v1beta1", "kind":"TargetGroupBinding", "metadata":map[string]interface{}{"name":"api-tgb", "namespace":"prod"}, "spec":map[string]interface{}{"serviceRef":map[string]interface{}{"name":"api", "port":int64(8080)}, "targetGroupARN":"arn:aws:elasticloadbalancing:region:account:targetgroup/example/123"}}}
 dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr:"TargetGroupBindingList"}, binding)
 backend := urlTraceBackend{Namespace:"prod", Service:"api", Port:"http", ServicePort:8080}
 limitations := []string{}
 resolveTargetGroupBindings(context.Background(), dyn, "prod", &backend, &limitations)
 if len(backend.TargetGroupBindings) != 1 { t.Fatalf("expected named Service port binding, got %+v", backend.TargetGroupBindings) }
}

func TestPodDiagnosisBudgetLimit(t *testing.T) {
 client := fake.NewSimpleClientset()
 budget := 3
 pod := &urlTracePod{Name:"limited"}
 diagnoseURLPodWithBudget(context.Background(), client, "prod", pod, &budget)
 if budget != 3 || len(pod.Findings) != 1 || !strings.Contains(pod.Findings[0], "limit reached") { t.Fatalf("expected diagnosis cap, budget=%d findings=%v", budget, pod.Findings) }
}

func TestHTTPRouteStatusConditions(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"parents": []interface{}{map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type":"Accepted", "status":"True"},
				map[string]interface{}{"type":"ResolvedRefs", "status":"False", "reason":"BackendNotFound"},
			},
		}}},
	}}
	status, notes := httpRouteStatus(route)
	if status != "FAILED" || len(notes) == 0 { t.Fatalf("expected failed route condition, got status=%s notes=%v", status, notes) }
}

func TestHTTPRouteCrossNamespaceReferenceGrant(t *testing.T) {
	grant := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion":"gateway.networking.k8s.io/v1",
		"kind":"ReferenceGrant",
		"metadata":map[string]interface{}{"name":"allow-frontend", "namespace":"backend"},
		"spec":map[string]interface{}{
			"from":[]interface{}{map[string]interface{}{"group":"gateway.networking.k8s.io", "kind":"HTTPRoute", "namespace":"frontend"}},
			"to":[]interface{}{map[string]interface{}{"group":"", "kind":"Service", "name":"api"}},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), grant)
	if !httpRouteReferenceAllowed(context.Background(), dyn, "frontend", "backend", "api") {
		t.Fatal("matching ReferenceGrant should allow the cross-namespace Service reference")
	}
	if httpRouteReferenceAllowed(context.Background(), dyn, "other", "backend", "api") {
		t.Fatal("ReferenceGrant must not authorize a different source namespace")
	}
}
