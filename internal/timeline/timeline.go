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
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
)

const revisionAnnotation = "deployment.kubernetes.io/revision"

type Analyzer struct {
	Client   kubernetes.Interface
	Dynamic  dynamic.Interface
	Metadata metadata.Interface
	Since    time.Duration
	FailureAt *time.Time
	MaxCorrelations int
	Now      func() time.Time
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
	sinceDuration := a.Since
	if sinceDuration <= 0 { sinceDuration = 24 * time.Hour }
	since := now.Add(-sinceDuration)
	report := &Report{
		Namespace: namespace, Target: "Deployment/" + name, GeneratedAt: now, Since: since,
		Events: make([]TimelineEvent, 0, len(owned)),
		Limitations: []string{
			"Only ReplicaSets still present in the Kubernetes API can be compared; deleted revision history is unavailable.",
			"ReplicaSet creation timestamps indicate rollout object creation, not the exact time each field was applied.",
			"Kubernetes Events are best-effort and may have expired according to cluster event-retention settings.",
			"ConfigMap/Secret resourceVersions are opaque; only current metadata and managedFields timestamps are available, not historical object versions.",
			"Environment variable values, Secret data, command contents, and argument contents are never included.",
			"This timeline reports observed changes and potential correlations; it does not establish causation.",
			"Timeline history is filtered to the selected --since window; older evidence is intentionally omitted.",
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
			SortTime: timePointer(current.CreationTimestamp.Time),
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
					event.SortTime = timePointer(lastConditionUpdate)
					event.Details += " Deployment condition lastUpdateTime is an approximation, not an audit timestamp."
				} else {
					event.Timestamp = nil
					sortTime := previousCreation.Add(time.Nanosecond)
					event.SortTime = &sortTime
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

	rolloutStart := currentRolloutStart(deployment, owned, currentRevision)
	if currentRevisionUsesOlderReplicaSet(owned, currentRevision) && rolloutStart.Equal(latestReplicaSetTime(owned)) {
		report.Limitations = appendUnique(report.Limitations, "Rollback time was unavailable; the latest retained ReplicaSet creation time is used as a conservative failure window boundary.")
	}
	addConfigResourceEvents(ctx, a.Metadata, report, owned, currentRevision, since)
	labels := map[string]string{}
	for key, value := range deployment.Labels { labels[key] = value }
	for key, value := range deployment.Spec.Template.Labels { labels[key] = value }
	addGitOpsEvents(ctx, a.Dynamic, report, labels)
	filterTimelineSince(report, since)

	activeUnhealthyPod := false
	currentReplicaSetUIDs := currentReplicaSetUIDs(owned, currentRevision)
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
			if !podOwnedByReplicaSet(pod, currentReplicaSetUIDs) || !podCurrentlyUnhealthy(pod) || failureTime == nil || failureTime.Before(rolloutStart) || failureTime.Before(since) { failureTime = nil }
			if failureTime != nil {
				activeUnhealthyPod = true
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

	activeFailure := activeUnhealthyPod
	events, eventErr := a.Client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if eventErr != nil {
		report.Limitations = appendUnique(report.Limitations, "Kubernetes Events could not be read; scaling and warning-event timestamps are unavailable.")
	} else {
		for _, item := range events.Items {
			_, relevant := ownedUIDs[string(item.InvolvedObject.UID)]
			if !relevant && item.InvolvedObject.Kind != "Pod" { continue }
			if item.InvolvedObject.Kind == "Pod" && !podEventBelongsToDeployment(item, podItems, owned) { continue }
			if !isRelevantDeploymentEvent(item) {
				continue
			}
			timestamp := eventTimestamp(&item)
			eventType := EventKubernetes
			action := item.Reason
			confidence := "observed"
			if item.Type == corev1.EventTypeWarning {
				eventType = EventFailure
				action = "Warning event: " + item.Reason
				if activeFailure && timestamp != nil && !timestamp.Before(rolloutStart) && !timestamp.Before(since) && warningIndicatesActiveFailure(item.Reason) && warningBelongsToCurrentUnhealthyWorkload(item, podItems, owned, currentReplicaSetUIDs) {
					report.FailureStartedAt = earlierTime(report.FailureStartedAt, timestamp)
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
	filterTimelineSince(report, since)
	if a.FailureAt != nil { report.FailureStartedAt = timePointer(*a.FailureAt) }
	buildCorrelations(report, a.MaxCorrelations)
	sort.SliceStable(report.Events, func(i, j int) bool {
		left, right := report.Events[i].SortTime, report.Events[j].SortTime
		if left == nil { left = report.Events[i].Timestamp }
		if right == nil { right = report.Events[j].Timestamp }
		if left == nil { return false }
		if right == nil { return true }
		return left.Before(*right)
	})
	return report, nil
}

func eventTimestamp(event *corev1.Event) *time.Time {
	values := []time.Time{}
	if event.Series != nil { values = append(values, event.Series.LastObservedTime.Time) }
	values = append(values, event.EventTime.Time, event.LastTimestamp.Time, event.FirstTimestamp.Time, event.CreationTimestamp.Time)
	for _, value := range values {
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

func podEventBelongsToDeployment(event corev1.Event, pods []corev1.Pod, replicaSets []appsv1.ReplicaSet) bool {
	for _, pod := range pods {
		if event.InvolvedObject.Name == pod.Name || (event.InvolvedObject.UID != "" && event.InvolvedObject.UID == pod.UID) { return true }
	}
	for _, rs := range replicaSets {
		if strings.HasPrefix(event.InvolvedObject.Name, rs.Name+"-") { return true }
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
		if terminated := status.LastTerminationState.Terminated; terminated != nil && !terminated.FinishedAt.IsZero() && status.RestartCount > 0 {
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

func latestReplicaSetTime(replicaSets []appsv1.ReplicaSet) time.Time {
	var latest time.Time
	for _, rs := range replicaSets {
		if rs.CreationTimestamp.Time.After(latest) { latest = rs.CreationTimestamp.Time }
	}
	return latest
}

func podCurrentlyUnhealthy(pod corev1.Pod) bool {
	if pod.Status.Phase == corev1.PodFailed { return true }
	if pod.Status.Phase == corev1.PodPending {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == "Unschedulable" { return true }
		}
	}
	if pod.Status.Phase == corev1.PodRunning && !podReady(pod) { return true }
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		if status.State.Waiting == nil { continue }
		switch status.State.Waiting.Reason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "RunContainerError":
			return true
		}
	}
	return false
}

func earlierTime(current, candidate *time.Time) *time.Time {
	if candidate == nil { return current }
	if current == nil || candidate.Before(*current) { return candidate }
	return current
}

func warningIndicatesActiveFailure(reason string) bool {
	switch reason {
	case "SuccessfulCreate", "SuccessfulDelete":
		return false
	default:
		return true
	}
}

func warningBelongsToCurrentUnhealthyWorkload(event corev1.Event, pods []corev1.Pod, replicaSets []appsv1.ReplicaSet, currentReplicaSets map[string]struct{}) bool {
	if event.InvolvedObject.Kind == "Pod" {
		for _, pod := range pods {
			if event.InvolvedObject.Name != pod.Name && (event.InvolvedObject.UID == "" || event.InvolvedObject.UID != pod.UID) { continue }
			if !podOwnedByReplicaSet(pod, currentReplicaSets) { return false }
			return podCurrentlyUnhealthy(pod)
		}
		return false
	}
	if event.InvolvedObject.Kind == "ReplicaSet" {
		for _, rs := range replicaSets {
			if rs.Name != event.InvolvedObject.Name { continue }
			if _, current := currentReplicaSets[string(rs.UID)]; !current { return false }
			for _, pod := range pods {
				if podOwnedByReplicaSet(pod, map[string]struct{}{string(rs.UID): {}}) && podCurrentlyUnhealthy(pod) { return true }
			}
		}
	}
	return false
}

func isRelevantDeploymentEvent(event corev1.Event) bool {
	if event.Type == corev1.EventTypeWarning { return warningIndicatesActiveFailure(event.Reason) }
	switch event.Reason {
	case "ScalingReplicaSet", "SuccessfulCreate", "SuccessfulDelete":
		return true
	default:
		return false
	}
}

func filterTimelineSince(report *Report, since time.Time) {
	filtered := report.Events[:0]
	for _, event := range report.Events {
		if event.Timestamp == nil || !event.Timestamp.Before(since) { filtered = append(filtered, event) }
	}
	report.Events = filtered
}

func buildCorrelations(report *Report, maxResults int) {
	report.Correlations = nil
	if report.FailureStartedAt == nil { return }
	candidates := make([]Correlation, 0)
	for _, event := range report.Events {
		if event.Timestamp == nil || !event.Timestamp.Before(*report.FailureStartedAt) { continue }
		if event.Type != EventRollout && event.Type != EventConfig && event.Type != EventGitOps { continue }
		delta := report.FailureStartedAt.Sub(*event.Timestamp)
		score := 0
		switch {
		case delta <= 15*time.Minute: score += 40
		case delta <= time.Hour: score += 30
		case delta <= 6*time.Hour: score += 20
		case delta <= 24*time.Hour: score += 10
		}
		switch event.Type {
		case EventRollout: score += 35
		case EventConfig: score += 35
		case EventGitOps: score += 20
		}
		if len(event.Changes) > 0 { score += 20 }
		if event.Current { score += 5 }
		summary := event.Action
		if len(event.Changes) > 0 {
			summary += ": "
			for i, change := range event.Changes {
				if i > 0 { summary += "; " }
				summary += change.Field + " " + change.Before + " -> " + change.After
			}
		}
		confidence := "low temporal relevance"
		if delta <= 15*time.Minute { confidence = "high temporal relevance" } else if delta <= time.Hour { confidence = "medium temporal relevance" }
		candidates = append(candidates, Correlation{
			Timestamp: event.Timestamp, Type: event.Type, Resource: event.Resource,
			Summary: summary, Delta: delta.Round(time.Second).String() + " before failure", Confidence: confidence, Score: score,
			Disclaimer: "Potential correlation only; ranking uses resource relevance, available change evidence and time proximity. Causation is not established.",
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score { return candidates[i].Score > candidates[j].Score }
		return candidates[i].Timestamp.After(*candidates[j].Timestamp)
	})
	if maxResults <= 0 { maxResults = 5 }
	if len(candidates) > maxResults { candidates = candidates[:maxResults] }
	for i := range candidates { candidates[i].Rank = i + 1 }
	report.Correlations = candidates
}

func currentRolloutStart(deployment *appsv1.Deployment, replicaSets []appsv1.ReplicaSet, currentRevision string) time.Time {
	var revisionCreation time.Time
	for _, rs := range replicaSets {
		if currentRevision != "" && rs.Annotations[revisionAnnotation] == currentRevision {
			revisionCreation = rs.CreationTimestamp.Time
			break
		}
	}
	if revisionCreation.IsZero() { revisionCreation = latestReplicaSetTime(replicaSets) }
	var conditionUpdate time.Time
	for _, condition := range deployment.Status.Conditions {
		if condition.LastUpdateTime.Time.After(conditionUpdate) { conditionUpdate = condition.LastUpdateTime.Time }
	}
	newestCreation := latestReplicaSetTime(replicaSets)
	if !revisionCreation.IsZero() && revisionCreation.Before(newestCreation) {
		if conditionUpdate.After(newestCreation) { return conditionUpdate }
		return newestCreation
	}
	if revisionCreation.IsZero() { return conditionUpdate }
	return revisionCreation
}

func currentReplicaSetUIDs(replicaSets []appsv1.ReplicaSet, currentRevision string) map[string]struct{} {
	result := map[string]struct{}{}
	if currentRevision != "" {
		for _, rs := range replicaSets {
			if rs.Annotations[revisionAnnotation] == currentRevision { result[string(rs.UID)] = struct{}{} }
		}
	}
	if len(result) == 0 && len(replicaSets) > 0 {
		latest := replicaSets[0]
		for _, rs := range replicaSets[1:] {
			if rs.CreationTimestamp.Time.After(latest.CreationTimestamp.Time) { latest = rs }
		}
		result[string(latest.UID)] = struct{}{}
	}
	return result
}

func currentRevisionUsesOlderReplicaSet(replicaSets []appsv1.ReplicaSet, currentRevision string) bool {
	newest := latestReplicaSetTime(replicaSets)
	for _, rs := range replicaSets {
		if currentRevision != "" && rs.Annotations[revisionAnnotation] == currentRevision {
			return rs.CreationTimestamp.Time.Before(newest)
		}
	}
	return false
}
