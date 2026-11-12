package timeline

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/dynamic"
)

const revisionAnnotation = "deployment.kubernetes.io/revision"

type Analyzer struct {
	Client  kubernetes.Interface
	Dynamic dynamic.Interface
	Now     func() time.Time
}

func (a *Analyzer) AnalyzeDeployment(ctx context.Context, namespace, name string) (*Report, error) {
	if a.Client == nil {
		return nil, fmt.Errorf("Kubernetes client is required")
	}
	deployment, err := a.Client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	if deployment.Spec.Selector == nil {
		return nil, fmt.Errorf("deployment %s/%s has no selector", namespace, name)
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("deployment has invalid selector: %w", err)
	}
	replicaSets, err := a.Client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, fmt.Errorf("list ReplicaSets for deployment %s/%s: %w", namespace, name, err)
	}

	owned := make([]appsv1.ReplicaSet, 0, len(replicaSets.Items))
	ownedUIDs := map[string]struct{}{string(deployment.UID): {}}
	for _, rs := range replicaSets.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.Controller != nil && *owner.Controller && owner.UID == deployment.UID && owner.Kind == "Deployment" {
				owned = append(owned, rs)
				ownedUIDs[string(rs.UID)] = struct{}{}
				break
			}
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		ri, errI := strconv.Atoi(owned[i].Annotations[revisionAnnotation])
		rj, errJ := strconv.Atoi(owned[j].Annotations[revisionAnnotation])
		if errI == nil && errJ == nil && ri != rj {
			return ri < rj
		}
		if errI == nil && errJ != nil {
			return true
		}
		if errI != nil && errJ == nil {
			return false
		}
		return owned[i].CreationTimestamp.Before(&owned[j].CreationTimestamp)
	})

	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	report := &Report{
		Namespace: namespace, Target: "Deployment/" + name, GeneratedAt: now,
		Events: make([]TimelineEvent, 0, len(owned)),
		Limitations: []string{
			"Only ReplicaSets still present in the Kubernetes API can be compared; deleted revision history is unavailable.",
			"ReplicaSet creation timestamps indicate rollout object creation, not the exact time each field was applied.",
			"Kubernetes Events are best-effort and may have expired according to cluster event-retention settings.",
			"Environment variable values, Secret data, command contents, and argument contents are never included.",
			"This timeline reports observed changes and potential correlations; it does not establish causation.",
		},
	}
	currentRevision := deployment.Annotations[revisionAnnotation]
	var previous *appsv1.ReplicaSet
	var previousCreation time.Time
	var lastConditionUpdate time.Time
	for _, condition := range deployment.Status.Conditions {
		if condition.LastUpdateTime.Time.After(lastConditionUpdate) {
			lastConditionUpdate = condition.LastUpdateTime.Time
		}
	}
	seenRevisions := map[int]struct{}{}
	for i := range owned {
		current := &owned[i]
		revision := current.Annotations[revisionAnnotation]
		if revision == "" {
			revision = "unknown"
			report.Limitations = appendUnique(report.Limitations, "One or more ReplicaSets lack the revision annotation; those entries are ordered by creation timestamp.")
		} else if number, parseErr := strconv.Atoi(revision); parseErr == nil {
			seenRevisions[number] = struct{}{}
		}
		event := TimelineEvent{
			Timestamp: timePointer(current.CreationTimestamp.Time),
			Type: EventRollout,
			Resource: "ReplicaSet/" + current.Name,
			Revision: revision,
			Current: revision != "unknown" && revision == currentRevision,
			Action: "Deployment revision observed",
			Source: "replicaset-template",
			Confidence: "observed",
		}
		if previous != nil {
			if current.CreationTimestamp.Time.Before(previousCreation) {
				event.Details = "An older ReplicaSet was reused (rollback likely). Exact rollback time is unavailable."
				report.Limitations = appendUnique(report.Limitations, "Rollback reused an older ReplicaSet; its creation timestamp is historical, not the rollback time.")
				if lastConditionUpdate.After(previousCreation) && !lastConditionUpdate.After(now) {
					event.Timestamp = timePointer(lastConditionUpdate)
					event.Details += " Deployment condition lastUpdateTime is an approximation, not an audit timestamp."
				} else {
					event.Timestamp = nil
				}
			}
			event.Changes = compareTemplates(previous, current)
			if len(event.Changes) == 0 {
				event.Action = "Revision observed; no supported template differences"
			} else {
				event.Action = fmt.Sprintf("%d template change(s) from previous retained revision", len(event.Changes))
			}
		} else {
			event.Details = "Earliest retained ReplicaSet; no earlier retained revision is available for comparison."
		}
		report.Events = append(report.Events, event)
		previous, previousCreation = current, current.CreationTimestamp.Time
	}
	if len(owned) == 0 {
		report.Limitations = append(report.Limitations, "No ReplicaSets controlled by this Deployment were found.")
	}
	if len(seenRevisions) > 0 {
		minRevision, maxRevision := int(^uint(0)>>1), 0
		for revision := range seenRevisions {
			if revision < minRevision { minRevision = revision }
			if revision > maxRevision { maxRevision = revision }
		}
		missing := make([]string, 0)
		for revision := minRevision; revision <= maxRevision; revision++ {
			if _, ok := seenRevisions[revision]; !ok {
				missing = append(missing, strconv.Itoa(revision))
			}
		}
		if len(missing) > 0 {
			gapDetail := "Revision gap(s) detected: " + joinValues(missing, ", ") + ". Missing revision contents cannot be reconstructed."
			for _, event := range report.Events {
				if strings.Contains(event.Details, "older ReplicaSet was reused") && event.Revision != "" {
					gapDetail += " Revision " + joinValues(missing, ", ") + " may have been reused as revision " + event.Revision + " during rollback; the original revision entry is unavailable."
					break
				}
			}
			report.Limitations = appendUnique(report.Limitations, gapDetail)
		}
	}

	addConfigResourceEvents(ctx, a.Client, report, owned)
	labels := map[string]string{}
	for key, value := range deployment.Labels { labels[key] = value }
	for key, value := range deployment.Spec.Template.Labels { labels[key] = value }
	addGitOpsEvents(ctx, a.Dynamic, report, labels)

	pods, podErr := a.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	podItems := []corev1.Pod{}
	if podErr == nil && pods != nil {
		podItems = pods.Items
		for _, pod := range podItems {
			if !podOwnedByReplicaSet(pod, ownedUIDs) {
				continue
			}
			var restarts int32
			var lastRestart time.Time
			for _, status := range pod.Status.ContainerStatuses {
				restarts += status.RestartCount
				if terminated := status.LastTerminationState.Terminated; terminated != nil && terminated.FinishedAt.Time.After(lastRestart) {
					lastRestart = terminated.FinishedAt.Time
				}
			}
			if restarts > 0 {
				report.Events = append(report.Events, TimelineEvent{
					Timestamp: timePointer(lastRestart), Type: EventRestart,
					Resource: "Pod/" + pod.Name, Action: fmt.Sprintf("%d container restart(s) observed", restarts),
					Source: "pod-container-status", Confidence: "observed",
					Details: "RestartCount is cumulative for the current Pod; timestamp is the latest retained previous termination time when available.",
				})
			}
			failureTime := podFailureStart(pod)
			if failureTime != nil {
				report.Events = append(report.Events, TimelineEvent{
					Timestamp: failureTime, Type: EventFailure, Resource: "Pod/" + pod.Name,
					Action: "Pod failure or degradation start observed", Source: "pod-status",
					Confidence: "observed", Details: fmt.Sprintf("phase=%s ready=%t restarts=%d", pod.Status.Phase, podReady(pod), restarts),
				})
				if report.FailureStartedAt == nil || failureTime.Before(*report.FailureStartedAt) {
					report.FailureStartedAt = failureTime
				}
			}
		}
	} else {
		report.Limitations = appendUnique(report.Limitations, "Pod status could not be read; failure-start and restart timeline entries are incomplete.")
	}

	events, eventErr := a.Client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if eventErr != nil {
		report.Limitations = appendUnique(report.Limitations, "Kubernetes Events could not be read; scaling and warning-event timestamps are unavailable.")
	} else {
		for _, item := range events.Items {
			_, relevant := ownedUIDs[string(item.InvolvedObject.UID)]
			if !relevant && item.InvolvedObject.Kind != "Pod" {
				continue
			}
			if item.InvolvedObject.Kind == "Pod" && !podMatchesDeployment(item.InvolvedObject.Name, podItems) {
				continue
			}
			if item.Type != corev1.EventTypeWarning && item.Reason != "ScalingReplicaSet" && item.Reason != "SuccessfulCreate" && item.Reason != "SuccessfulDelete" {
				continue
			}
			timestamp := eventTimestamp(&item)
			eventType := EventKubernetes
			action := item.Reason
			confidence := "observed"
			if item.Type == corev1.EventTypeWarning {
				eventType = EventFailure
				action = "Warning event: " + item.Reason
				if report.FailureStartedAt == nil || (timestamp != nil && timestamp.Before(*report.FailureStartedAt)) {
					report.FailureStartedAt = timestamp
				}
			}
			report.Events = append(report.Events, TimelineEvent{
				Timestamp: timestamp, Type: eventType,
				Resource: item.InvolvedObject.Kind + "/" + item.InvolvedObject.Name,
				Action: action, Source: "kubernetes-event", Confidence: confidence,
				Details: fmt.Sprintf("%s (count=%d): %s", item.Type, maxEventCount(item), item.Message),
			})
		}
	}
	sort.SliceStable(report.Events, func(i, j int) bool {
		if report.Events[i].Timestamp == nil { return false }
		if report.Events[j].Timestamp == nil { return true }
		return report.Events[i].Timestamp.Before(*report.Events[j].Timestamp)
	})
	return report, nil
}

func eventTimestamp(event *corev1.Event) *time.Time {
	for _, value := range []time.Time{event.EventTime.Time, event.LastTimestamp.Time, event.FirstTimestamp.Time, event.CreationTimestamp.Time} {
		if !value.IsZero() { return timePointer(value) }
	}
	return nil
}

func maxEventCount(event corev1.Event) int32 {
	if event.Count < 1 { return 1 }
	return event.Count
}

func podOwnedByReplicaSet(pod corev1.Pod, owners map[string]struct{}) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "ReplicaSet" {
			if _, ok := owners[string(owner.UID)]; ok { return true }
		}
	}
	return false
}

func podMatchesDeployment(name string, pods []corev1.Pod) bool {
	for _, pod := range pods {
		if pod.Name == name { return true }
	}
	return false
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady { return condition.Status == corev1.ConditionTrue }
	}
	return false
}

func podFailureStart(pod corev1.Pod) *time.Time {
	var earliest time.Time
	for _, condition := range pod.Status.Conditions {
		if condition.Status == corev1.ConditionFalse && (condition.Type != corev1.PodReady || pod.Status.Phase == corev1.PodRunning) && !condition.LastTransitionTime.IsZero() {
			if earliest.IsZero() || condition.LastTransitionTime.Time.Before(earliest) { earliest = condition.LastTransitionTime.Time }
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if terminated := status.LastTerminationState.Terminated; terminated != nil && !terminated.FinishedAt.IsZero() {
			if earliest.IsZero() || terminated.FinishedAt.Time.Before(earliest) { earliest = terminated.FinishedAt.Time }
		}
	}
	return timePointer(earliest)
}

func joinValues(values []string, separator string) string {
	result := ""
	for i, value := range values {
		if i > 0 { result += separator }
		result += value
	}
	return result
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() { return nil }
	return &value
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value { return items }
	}
	return append(items, value)
}
