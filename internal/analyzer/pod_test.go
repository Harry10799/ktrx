package analyzer

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestContainerFailureEvidenceDistinguishesRecoveredOOM(t *testing.T) {
	previous := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: time.Now()}
	status := corev1.ContainerStatus{
		Name: "app", Ready: true, RestartCount: 2,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
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
		Name: "app",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	}
	evidence := containerFailureEvidence("app", status)
	if len(evidence) != 1 || evidence[0].Verdict != Failed {
		t.Fatalf("current OOM should fail, got %+v", evidence)
	}
	for _, detail := range evidence[0].Details {
		if detail == "message=" { t.Fatalf("empty message should be omitted: %+v", evidence[0].Details) }
	}
}
