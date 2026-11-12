package timeline

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCompareTemplatesDetectsProbeCommandPortsAndInitContainer(t *testing.T) {
	old := &appsv1.ReplicaSet{
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "api",
						Command: []string{"serve"},
						Ports:   []corev1.ContainerPort{{ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt(8080)},
							},
						},
					}},
					InitContainers: []corev1.Container{{Name: "migrate", Image: "migrate:v1"}},
				},
			},
		},
	}
	next := old.DeepCopy()
	next.Spec.Template.Spec.Containers[0].Command = []string{"serve", "--password=do-not-print"}
	next.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort = 9090
	next.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path = "/health"
	next.Spec.Template.Spec.InitContainers[0].Image = "migrate:v2"

	changes := compareTemplates(old, next)
	for _, field := range []string{
		"containers.api.command",
		"containers.api.ports",
		"containers.api.readinessProbe",
		"initContainers.migrate.image",
	} {
		found := false
		for _, change := range changes {
			if change.Field == field {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s diff: %+v", field, changes)
		}
	}
	for _, change := range changes {
		if strings.Contains(change.Before, "password") || strings.Contains(change.After, "password") {
			t.Fatalf("command leaked: %+v", change)
		}
	}
}

func TestCompareEnvShowsReferenceNamesNotValues(t *testing.T) {
	var changes []Change
	old := []corev1.EnvVar{{
		Name: "DB_PASSWORD",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "db-old"},
				Key:                  "PASSWORD",
			},
		},
	}}
	next := []corev1.EnvVar{{
		Name: "DB_PASSWORD",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "db-new"},
				Key:                  "PASSWORD",
			},
		},
	}}

	compareEnv(&changes, "containers.api", old, next)
	if len(changes) != 1 || changes[0].Before != "secret/db-old/PASSWORD" || changes[0].After != "secret/db-new/PASSWORD" {
		t.Fatalf("unexpected reference diff: %+v", changes)
	}
}

func TestAnalyzeDeploymentRollbackTimestampAndCurrentMarker(t *testing.T) {
	controller := true
	oldTime := metav1.NewTime(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	newTime := metav1.NewTime(time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "orders",
			Namespace:   "prod",
			UID:         types.UID("dep"),
			Annotations: map[string]string{revisionAnnotation: "3"},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "orders"}},
		},
	}
	makeReplicaSet := func(name, revision string, created metav1.Time) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "prod",
				CreationTimestamp: created,
				Labels:            map[string]string{"app": "orders"},
				Annotations:       map[string]string{revisionAnnotation: revision},
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "Deployment",
					Name:       "orders",
					UID:        deployment.UID,
					Controller: &controller,
				}},
			},
		}
	}

	// A real rollback reuses the old ReplicaSet and updates its revision
	// annotation; it does not create a third ReplicaSet.
	client := fake.NewSimpleClientset(
		deployment,
		makeReplicaSet("newer-rs", "2", newTime),
		makeReplicaSet("reused-old-rs", "3", oldTime),
	)
	report, err := (&Analyzer{
		Client: client,
		Now:    func() time.Time { return newTime.Add(time.Hour) },
	}).AnalyzeDeployment(context.Background(), "prod", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Events) != 2 {
		t.Fatalf("events=%d", len(report.Events))
	}
	rollbackEvent := report.Events[1]
	if !rollbackEvent.Current {
		t.Fatal("current revision not marked")
	}
	if rollbackEvent.Timestamp != nil || !strings.Contains(rollbackEvent.Details, "rollback likely") {
		t.Fatalf("expected honest rollback timestamp: %+v", rollbackEvent)
	}
}

func TestAnalyzeDeploymentRejectsMissingSelector(t *testing.T) {
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default", UID: types.UID("d")},
	})
	if _, err := (&Analyzer{Client: client}).AnalyzeDeployment(context.Background(), "default", "bad"); err == nil {
		t.Fatal("expected selector error")
	}
}

func TestBuildCorrelationsRanksEvidenceAndTimeTogether(t *testing.T) {
	failure := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	recentGitOps := failure.Add(-5 * time.Minute)
	olderRollout := failure.Add(-2 * time.Hour)
	report := &Report{
		FailureStartedAt: &failure,
		Events: []TimelineEvent{
			{Timestamp:&recentGitOps, Type:EventGitOps, Resource:"ArgoCD Application/argocd/orders", Action:"GitOps revision observed"},
			{Timestamp:&olderRollout, Type:EventRollout, Resource:"ReplicaSet/orders-2", Action:"Deployment revision changed", Changes:[]Change{{Field:"image", Before:"v1", After:"v2"}}},
		},
	}
	buildCorrelations(report, 5)
	if len(report.Correlations) != 2 { t.Fatalf("got %d correlations", len(report.Correlations)) }
	if report.Correlations[0].Type != EventRollout {
		t.Fatalf("change-rich rollout should outrank recency-only GitOps event: %+v", report.Correlations)
	}
	if report.Correlations[0].Confidence != "low temporal relevance" { t.Fatalf("5-hour-old event must have low temporal confidence, got %q", report.Correlations[0].Confidence) }
	if report.Correlations[0].Delta != "2h0m0s before failure" { t.Fatalf("unexpected time delta: %q", report.Correlations[0].Delta) }
	if report.Correlations[0].Score <= report.Correlations[1].Score {
		t.Fatalf("expected descending evidence score: %+v", report.Correlations)
	}
	if !strings.Contains(report.Correlations[0].Disclaimer, "Causation is not established") {
		t.Fatal("correlation disclaimer missing")
	}
}

func TestWarningEvidenceIncludesUnhealthyAndFailedSchedulingWhenPodIsActive(t *testing.T) {
	for _, reason := range []string{"Unhealthy", "FailedScheduling", "BackOff"} {
		if !warningIndicatesActiveFailure(reason) { t.Errorf("%s should remain visible as actionable warning evidence", reason) }
	}
	for _, reason := range []string{"SuccessfulCreate", "SuccessfulDelete"} {
		if warningIndicatesActiveFailure(reason) { t.Errorf("%s must not count as failure evidence", reason) }
	}
	controller := true
	rs := appsv1.ReplicaSet{ObjectMeta:metav1.ObjectMeta{Name:"api-rs", UID:types.UID("rs-uid")}}
	pod := corev1.Pod{
		ObjectMeta:metav1.ObjectMeta{Name:"api-rs-abc", UID:types.UID("pod-uid"), OwnerReferences:[]metav1.OwnerReference{{Kind:"ReplicaSet", UID:rs.UID, Controller:&controller}}},
		Status:corev1.PodStatus{Phase:corev1.PodPending, Conditions:[]corev1.PodCondition{{Type:corev1.PodScheduled, Status:corev1.ConditionFalse, Reason:"Unschedulable"}}},
	}
	event := corev1.Event{InvolvedObject:corev1.ObjectReference{Kind:"Pod", Name:pod.Name, UID:pod.UID}, Reason:"FailedScheduling", Type:corev1.EventTypeWarning}
	current := map[string]struct{}{string(rs.UID):{}}
	if !podCurrentlyUnhealthy(pod) { t.Fatal("unschedulable Pending Pod should be unhealthy evidence") }
	if !warningBelongsToCurrentUnhealthyWorkload(event, []corev1.Pod{pod}, []appsv1.ReplicaSet{rs}, current) {
		t.Fatal("FailedScheduling event should match active unhealthy Pod in current ReplicaSet")
	}
}
