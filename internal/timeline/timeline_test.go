package timeline

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAnalyzeDeploymentUsesOnlyOwnedReplicaSetsAndComparesRevisions(t *testing.T) {
	controller := true
	created1 := metav1.NewTime(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	created2 := metav1.NewTime(time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC))
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "production", UID: types.UID("deployment-uid")},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "orders"}}},
	}
	makeRS := func(name, revision, image string, created metav1.Time, ownerUID types.UID) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "production", CreationTimestamp: created,
				Labels: map[string]string{"app": "orders"},
				Annotations: map[string]string{revisionAnnotation: revision},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "orders", UID: ownerUID, Controller: &controller}},
			},
			Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: image}}}}},
		}
	}
	client := fake.NewSimpleClientset(
		deployment,
		makeRS("orders-old", "1", "orders:v1", created1, deployment.UID),
		makeRS("orders-new", "2", "orders:v2", created2, deployment.UID),
		makeRS("other-deployment", "3", "orders:v3", created2, types.UID("another-deployment")),
	)
	report, err := (&Analyzer{Client: client, Now: func() time.Time { return created2.Time }}).AnalyzeDeployment(context.Background(), "production", "orders")
	if err != nil {
		t.Fatalf("AnalyzeDeployment() error = %v", err)
	}
	if len(report.Events) != 2 {
		t.Fatalf("got %d timeline events, want 2 owned ReplicaSets", len(report.Events))
	}
	if report.Events[0].Revision != "1" || report.Events[1].Revision != "2" {
		t.Fatalf("unexpected revision order: %+v", report.Events)
	}
	if len(report.Events[1].Changes) != 1 || report.Events[1].Changes[0].Field != "containers.api.image" {
		t.Fatalf("expected image diff in second revision, got %+v", report.Events[1].Changes)
	}
}
