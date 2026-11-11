package analyzer

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestContainerFailureEvidenceDistinguishesRecoveredOOM(t *testing.T) {
	previous := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}
	status := corev1.ContainerStatus{
		Name: "app", Ready: true, RestartCount: 2,
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: previous},
	}
	evidence := containerFailureEvidence("app", status)
	if len(evidence) != 1 || evidence[0].Verdict != Warning {
		t.Fatalf("recovered OOM should be warning, got %+v", evidence)
	}

	status.Ready = false
	evidence = containerFailureEvidence("app", status)
	if len(evidence) != 1 || evidence[0].Verdict != Failed {
		t.Fatalf("unready container with previous OOM should fail, got %+v", evidence)
	}
}

func TestContainerFailureEvidenceCurrentOOMFailsAndSkipsEmptyMessage(t *testing.T) {
	status := corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	}
	evidence := containerFailureEvidence("app", status)
	if len(evidence) != 1 || evidence[0].Verdict != Failed {
		t.Fatalf("current OOM should fail, got %+v", evidence)
	}
	for _, detail := range evidence[0].Details {
		if detail == "message=" {
			t.Fatalf("empty message should be omitted: %+v", evidence[0].Details)
		}
	}
}

func TestAggregateIgnoresNonCriticalUnknownChecks(t *testing.T) {
	items := []Evidence{
		{Check: "pod-phase", Verdict: Healthy},
		{Check: "probe/liveness/app", Verdict: Unknown},
	}
	if got := aggregate(items); got != Healthy {
		t.Fatalf("aggregate() = %s, want HEALTHY when only optional checks are unknown", got)
	}
}

func TestAggregateKeepsCriticalUnknownChecksUnknown(t *testing.T) {
	items := []Evidence{
		{Check: "pod-phase", Verdict: Healthy},
		{Check: "pod-events", Verdict: Unknown},
	}
	if got := aggregate(items); got != Unknown {
		t.Fatalf("aggregate() = %s, want UNKNOWN when event collection failed", got)
	}
}

func TestAggregateWarningsOutrankNonCriticalUnknown(t *testing.T) {
	items := []Evidence{
		{Check: "probe/liveness/app", Verdict: Unknown},
		{Check: "image-tag/app", Verdict: Warning},
	}
	if got := aggregate(items); got != Warning {
		t.Fatalf("aggregate() = %s, want WARN", got)
	}
}

func TestContainerFailureEvidenceDetectsCrashLoopWaitingState(t *testing.T) {
	status := corev1.ContainerStatus{
		Name: "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff", Message: "back-off restarting failed container",
		}},
	}
	evidence := containerFailureEvidence("app", status)
	if len(evidence) != 1 || evidence[0].Verdict != Failed {
		t.Fatalf("CrashLoopBackOff waiting state should fail, got %+v", evidence)
	}
	if evidence[0].Check != "container-state/app" {
		t.Fatalf("unexpected check name: %+v", evidence[0])
	}
}
