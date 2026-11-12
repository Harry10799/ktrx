package timeline

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestCompareTemplatesReportsRolloutChangesWithoutLeakingEnvValues(t *testing.T) {
	before := &appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api", Image: "api:v1",
		Env: []corev1.EnvVar{{Name: "TOKEN", Value: "old-secret-value"}},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}},
	}}}}}}
	after := &appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api", Image: "api:v2",
		Env: []corev1.EnvVar{{Name: "TOKEN", Value: "new-secret-value"}},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}},
	}}}}}}

	changes := compareTemplates(before, after)
	if len(changes) != 3 {
		t.Fatalf("got %d changes, want image, memory limit and env name change: %+v", len(changes), changes)
	}
	for _, change := range changes {
		if change.Before == "old-secret-value" || change.After == "new-secret-value" {
			t.Fatalf("environment value leaked in change: %+v", change)
		}
	}
}

func TestCompareTemplatesDetectsAddedAndRemovedContainers(t *testing.T) {
	before := &appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "old"}}}}}}
	after := &appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "new"}}}}}}
	changes := compareTemplates(before, after)
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want added and removed container: %+v", len(changes), changes)
	}
}
