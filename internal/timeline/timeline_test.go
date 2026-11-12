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

func TestAnalyzeDeploymentUsesCurrentRolloutFailuresAndRetainsScalingEvents(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	controller := true
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orders", Namespace: "production", UID: types.UID("deployment-uid"),
			Annotations: map[string]string{revisionAnnotation: "2"},
		},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "orders"}}},
		Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
			Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue,
			LastUpdateTime: metav1.NewTime(now.Add(-time.Hour)),
		}}},
	}
	makeRS := func(name, revision string, created time.Time, uid types.UID) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "production", UID: uid, CreationTimestamp: metav1.NewTime(created),
				Annotations: map[string]string{revisionAnnotation: revision},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "orders", UID: deployment.UID, Controller: &controller}},
			},
		}
	}
	currentRS := makeRS("orders-current", "2", now.Add(-time.Hour), "rs-current")
	oldRS := makeRS("orders-old", "1", now.Add(-48*time.Hour), "rs-old")
	currentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-current-abcde", Namespace: "production", UID: "pod-current", Labels: map[string]string{"app": "orders"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: currentRS.Name, UID: currentRS.UID}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(now.Add(-10*time.Minute))}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "api", RestartCount: 3,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(now.Add(-9*time.Minute))}},
			}},
		},
	}
	recoveredPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-old-recovered", Namespace: "production", UID: "pod-old", Labels: map[string]string{"app": "orders"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: oldRS.Name, UID: oldRS.UID}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 1,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(now.Add(-47*time.Hour))}},
			}},
		},
	}
	event := func(eventName, involvedName, kind, reason, eventType string, at time.Time, uid types.UID) *corev1.Event {
		return &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: eventName, Namespace: "production", CreationTimestamp: metav1.NewTime(at)},
			InvolvedObject: corev1.ObjectReference{Kind: kind, Name: involvedName, UID: uid},
			Reason: reason, Type: eventType, EventTime: metav1.NewMicroTime(at), Message: reason,
		}
	}
	client := fake.NewSimpleClientset(
		deployment, currentRS, oldRS, currentPod, recoveredPod,
		event("backoff-event", "orders-current-abcde", "Pod", "BackOff", corev1.EventTypeWarning, now.Add(-7*time.Minute), currentPod.UID),
		event("unhealthy-event", "orders-current-abcde", "Pod", "Unhealthy", corev1.EventTypeWarning, now.Add(-5*time.Minute), currentPod.UID),
		event("scheduling-event", "orders-current-abcde", "Pod", "FailedScheduling", corev1.EventTypeWarning, now.Add(-6*time.Minute), currentPod.UID),
		event("mount-event", "orders-current-deleted", "Pod", "FailedMount", corev1.EventTypeWarning, now.Add(-8*time.Minute), "deleted-pod"),
		event("scale-event", "orders", "Deployment", "ScalingReplicaSet", corev1.EventTypeNormal, now.Add(-30*time.Minute), deployment.UID),
	)
	report, err := (&Analyzer{Client: client, Since: 24*time.Hour, Now: func() time.Time { return now }}).AnalyzeDeployment(context.Background(), "production", "orders")
	if err != nil { t.Fatalf("AnalyzeDeployment() error = %v", err) }
	if report.FailureStartedAt == nil || !report.FailureStartedAt.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("failure start should come from the current unhealthy Pod, got %v", report.FailureStartedAt)
	}
	foundScale, foundBackoff, foundDeletedPod := false, false, false
	for _, item := range report.Events {
		if item.Action == "ScalingReplicaSet" { foundScale = true }
		if item.Action == "Warning event: BackOff" { foundBackoff = true }
		if item.Resource == "Pod/orders-current-deleted" { foundDeletedPod = true }
		if item.Action == "Warning event: Unhealthy" || item.Action == "Warning event: FailedScheduling" {
			t.Fatalf("transient warning was included as failure evidence: %+v", item)
		}
	}
	if !foundScale || !foundBackoff || !foundDeletedPod {
		t.Fatalf("expected scale, active BackOff and deleted-Pod warning evidence; events=%+v", report.Events)
	}
}
