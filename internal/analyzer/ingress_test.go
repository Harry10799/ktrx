package analyzer

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestTraceIngressScopesMissingBackendToPod(t *testing.T) {
	tests := []struct {
		name string
		backend string
		podName string
		wantFailed bool
		wantNamespaceNote bool
	}{
		{name:"related missing backend fails", backend:"web", podName:"web-abc", wantFailed:true},
		{name:"unrelated missing backend is only a note", backend:"redis", podName:"web-abc", wantNamespaceNote:true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := &networkingv1.Ingress{
				ObjectMeta:metav1.ObjectMeta{Name:"sample", Namespace:"default"},
				Spec:networkingv1.IngressSpec{Rules:[]networkingv1.IngressRule{{IngressRuleValue:networkingv1.IngressRuleValue{HTTP:&networkingv1.HTTPIngressRuleValue{Paths:[]networkingv1.HTTPIngressPath{{Backend:networkingv1.IngressBackend{Service:&networkingv1.IngressServiceBackend{Name:tt.backend}}}}}}}}},
			}
			pod := &corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:tt.podName, Namespace:"default"}}
			client := fake.NewSimpleClientset(ing)
			_, evidence := traceIngress(context.Background(), client, pod, nil)
			failed, note := false, false
			for _, item := range evidence {
				if item.Verdict == Failed && item.Check == "ingress-backend/sample/"+tt.backend { failed = true }
				if item.Check == "ingress-namespace-note" && item.Verdict == Info { note = true }
			}
			if failed != tt.wantFailed { t.Fatalf("related backend failure=%t, want %t; evidence=%+v", failed, tt.wantFailed, evidence) }
			if note != tt.wantNamespaceNote { t.Fatalf("namespace note=%t, want %t; evidence=%+v", note, tt.wantNamespaceNote, evidence) }
		})
	}
}
