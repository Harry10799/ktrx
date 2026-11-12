package timeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metadatafake "k8s.io/client-go/metadata/fake"
)

func TestConfigMetadataUsesMetadataOnlyClientAndNeverSerializesSecretData(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	managed := metav1.NewTime(now.Add(-time.Hour))
	secret := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "db-credentials", Namespace: "production", ResourceVersion: "42",
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "external-secrets", Time: &managed}},
		},
	}
	scheme := metadatafake.NewTestScheme()
	client := metadatafake.NewSimpleMetadataClient(scheme, secret)
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "api-rs", Annotations: map[string]string{revisionAnnotation: "1"}}, Spec: appsv1.ReplicaSetSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-credentials"}}}},
			Containers: []corev1.Container{{Name: "api", Image: "api:v1"}},
		}},
	}}
	report := &Report{Namespace: "production", GeneratedAt: now, Events: []TimelineEvent{}}
	addConfigResourceEvents(context.Background(), client, report, []appsv1.ReplicaSet{rs}, "1", now.Add(-24*time.Hour))
	if len(report.Events) != 1 || report.Events[0].Resource != "Secret/db-credentials" {
		t.Fatalf("expected metadata-only Secret event, got %+v", report.Events)
	}
	encoded, err := json.Marshal(report)
	if err != nil { t.Fatal(err) }
	if strings.Contains(string(encoded), `"data":`) || strings.Contains(string(encoded), "super-secret-value") {
		t.Fatalf("secret payload leaked into report: %s", encoded)
	}
	actions := client.Actions()
	if len(actions) != 1 || actions[0].GetVerb() != "get" || actions[0].GetResource().Resource != "secrets" {
		t.Fatalf("expected a metadata API get for Secret, got %#v", actions)
	}
}

func TestMissingRequiredConfigMapIsFailure(t *testing.T) {
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "api-rs", Annotations: map[string]string{revisionAnnotation: "1"}}, Spec: appsv1.ReplicaSetSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-config"}}}}},
			Containers: []corev1.Container{{Name: "api", Image: "api:v1"}},
		}},
	}}
	scheme := metadatafake.NewTestScheme()
	client := metadatafake.NewSimpleMetadataClient(scheme)
	report := &Report{Namespace: "production", GeneratedAt: time.Now()}
	addConfigResourceEvents(context.Background(), client, report, []appsv1.ReplicaSet{rs}, "1", time.Now().Add(-24*time.Hour))
	if len(report.Events) != 1 || report.Events[0].Type != EventFailure {
		t.Fatalf("expected missing required ConfigMap failure, got %+v", report.Events)
	}
}
