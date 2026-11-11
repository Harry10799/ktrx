package analyzer

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestTraceInboundNearMatchRequiresStrongSignal(t *testing.T) {
	tests := []struct {
		name        string
		serviceName string
		selector    map[string]string
		podLabels   map[string]string
		wantWarning bool
	}{
		{name: "typo in existing label value", serviceName: "nginx", selector: map[string]string{"app": "nginxx"}, podLabels: map[string]string{"app": "nginx"}, wantWarning: true},
		{name: "different application is not a near match", serviceName: "redis", selector: map[string]string{"app": "redis"}, podLabels: map[string]string{"app": "nginx"}, wantWarning: false},
		{name: "selector key absent but value matches another pod label", serviceName: "nginx", selector: map[string]string{"name": "nginx"}, podLabels: map[string]string{"app": "nginx"}, wantWarning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: tt.serviceName, Namespace: "default"}, Spec: corev1.ServiceSpec{Selector: tt.selector}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nginx-abc", Namespace: "default", Labels: tt.podLabels}}
			client := fake.NewSimpleClientset(svc, pod, &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default", Labels: map[string]string{"kubernetes.io/service-name": tt.serviceName}}})
			_, evidence := traceInbound(context.Background(), client, pod)
			gotWarning := false
			for _, item := range evidence {
				if item.Check == "inbound-service-selector" && item.Verdict == Warning {
					gotWarning = true
				}
			}
			if gotWarning != tt.wantWarning {
				t.Fatalf("near-match warning = %t, want %t; evidence=%+v", gotWarning, tt.wantWarning, evidence)
			}
		})
	}
}

func TestCloseLabelValue(t *testing.T) {
	if !closeLabelValue("nginxx", "nginx") {
		t.Fatal("expected one-character typo to match")
	}
	if closeLabelValue("redis", "nginx") {
		t.Fatal("unrelated values must not match")
	}
	if closeLabelValue("v1", "v2") {
		t.Fatal("short values must not fuzzy-match")
	}
	if closeLabelValue("fe", "be") {
		t.Fatal("short values must not fuzzy-match")
	}
}

func TestTraceInboundServicesRBACDeniedProducesCriticalUnknown(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("forbidden"))
	})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"}}
	_, evidence := traceInbound(context.Background(), client, pod)
	if len(evidence) != 1 || evidence[0].Check != "inbound-services" || evidence[0].Verdict != Unknown {
		t.Fatalf("expected inbound-services UNKNOWN on RBAC denial, got %+v", evidence)
	}
	if got := aggregate(evidence); got != Unknown {
		t.Fatalf("aggregate() = %s, want UNKNOWN for Service RBAC denial", got)
	}
}
