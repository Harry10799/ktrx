package timeline

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestFailureStartIgnoresRecoveredPodAndOldTermination(t *testing.T) {
	pod := corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{
				RestartCount: 1,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(time.Now().Add(-48*time.Hour))}},
			}},
		},
	}
	if podCurrentlyUnhealthy(pod) {
		t.Fatal("a recovered Ready Pod must not be considered currently unhealthy")
	}
	if got := podFailureStart(pod); got == nil || time.Since(*got) < 47*time.Hour {
		t.Fatalf("expected retained historical termination timestamp, got %v", got)
	}
}

func TestFailureStartRecognizesActiveCrashLoop(t *testing.T) {
	pod := corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
	}}
	if !podCurrentlyUnhealthy(pod) {
		t.Fatal("CrashLoopBackOff must be considered an active failure")
	}
}

func TestWarningEventFilterRetainsContextualFailureWarnings(t *testing.T) {
	for _, reason := range []string{"Unhealthy", "FailedScheduling"} {
		if !isRelevantDeploymentEvent(corev1.Event{Type: corev1.EventTypeWarning, Reason: reason}) {
			t.Errorf("warning %q should remain visible for contextual failure evaluation", reason)
		}
	}
	if !isRelevantDeploymentEvent(corev1.Event{Type: corev1.EventTypeWarning, Reason: "FailedMount"}) {
		t.Fatal("FailedMount should remain a relevant warning")
	}
}

func TestDeletedPodEventsMatchRetainedReplicaSetName(t *testing.T) {
	event := corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "orders-rs-abcde", UID: types.UID("deleted-pod")}}
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "orders-rs"}}
	if !podEventBelongsToDeployment(event, nil, []appsv1.ReplicaSet{rs}) {
		t.Fatal("event for deleted Pod should match its retained ReplicaSet name prefix")
	}
}

func TestBuildCorrelationsRanksNearestChangeFirst(t *testing.T) {
	failure := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	older := failure.Add(-30 * time.Minute)
	newer := failure.Add(-2 * time.Minute)
	report := &Report{
		FailureStartedAt: &failure,
		Events: []TimelineEvent{
			{Timestamp: &older, Type: EventRollout, Resource: "ReplicaSet/old", Action: "older rollout"},
			{Timestamp: &newer, Type: EventConfig, Resource: "ConfigMap/app", Action: "config changed"},
		},
	}
	buildCorrelations(report)
	if len(report.Correlations) != 2 || report.Correlations[0].Resource != "ConfigMap/app" || report.Correlations[0].Rank != 1 {
		t.Fatalf("expected nearest change ranked first, got %+v", report.Correlations)
	}
	if report.Correlations[0].Disclaimer == "" {
		t.Fatal("correlation must state that causation is not established")
	}
}

func TestCurrentRolloutStartUsesConservativeBoundaryForRollback(t *testing.T) {
	oldCreation := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	newCreation := oldCreation.Add(time.Hour)
	deployment := &appsv1.Deployment{}
	replicaSets := []appsv1.ReplicaSet{
		{ObjectMeta: metav1.ObjectMeta{UID: "old", CreationTimestamp: metav1.NewTime(oldCreation), Annotations: map[string]string{revisionAnnotation: "4"}}},
		{ObjectMeta: metav1.ObjectMeta{UID: "new", CreationTimestamp: metav1.NewTime(newCreation), Annotations: map[string]string{revisionAnnotation: "3"}}},
	}
	got := currentRolloutStart(deployment, replicaSets, "4")
	if !got.Equal(newCreation) {
		t.Fatalf("without a rollback condition timestamp, want conservative boundary %s, got %s", newCreation, got)
	}
}
