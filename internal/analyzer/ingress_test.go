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
		name              string
		backend           string
		podName           string
		wantFailed        bool
		wantNamespaceNote bool
	}{
		{name: "related missing backend fails", backend: "web", podName: "web-abc", wantFailed: true},
		{name: "unrelated missing backend is only a note", backend: "redis", podName: "web-abc", wantNamespaceNote: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "default"},
				Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: tt.backend}}}}}}}}},
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tt.podName, Namespace: "default"}}
			client := fake.NewSimpleClientset(ing)
			_, evidence := traceIngress(context.Background(), client, pod, nil)
			failed, note := false, false
			for _, item := range evidence {
				if item.Verdict == Failed && item.Check == "ingress-backend/sample/"+tt.backend {
					failed = true
				}
				if item.Check == "ingress-namespace-note" && item.Verdict == Info {
					note = true
				}
			}
			if failed != tt.wantFailed {
				t.Fatalf("related backend failure=%t, want %t; evidence=%+v", failed, tt.wantFailed, evidence)
			}
			if note != tt.wantNamespaceNote {
				t.Fatalf("namespace note=%t, want %t; evidence=%+v", note, tt.wantNamespaceNote, evidence)
			}
		})
	}
}

func TestPromoteRoutedSelectorMismatch(t *testing.T) {
	path := networkingv1.HTTPIngressPath{Backend: networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{Name: "web"},
	}}
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{path}}},
		}}},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "default"}}
	evidence := []Evidence{{Check: "inbound-service-selector", Verdict: Warning,
		Details: []string{"Service web selector mismatch: app=web (pod has app=api) (Service currently has no endpoints)"}}}
	client := fake.NewSimpleClientset(ing)
	promoteRoutedSelectorMismatches(context.Background(), client, pod, evidence)
	if evidence[0].Verdict != Failed {
		t.Fatalf("Ingress-routed selector mismatch should fail, got %+v", evidence[0])
	}
}

func TestListIngressControllerPodsExcludesUnrelatedPods(t *testing.T) {
	controller := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ingress-controller", Namespace: "ingress-system", Labels: map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	app := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "application", Namespace: "default", Labels: map[string]string{"app": "web"}}}
	certManager := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager", Namespace: "cert-manager", Labels: map[string]string{"app.kubernetes.io/component": "controller", "app.kubernetes.io/name": "cert-manager"}}}
	worker := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "jobs", Labels: map[string]string{"role": "worker"}}}
	client := fake.NewSimpleClientset(controller, app, worker, certManager)

	controllers, err := listIngressControllerPods(context.Background(), client)
	if err != nil {
		t.Fatalf("listIngressControllerPods returned error: %v", err)
	}
	if len(controllers) != 1 {
		t.Fatalf("expected exactly 1 controller Pod, got %d: %+v", len(controllers), controllers)
	}
	if controllers[0].Name != "ingress-controller" {
		t.Fatalf("unexpected controller Pod: %s", controllers[0].Name)
	}
}
