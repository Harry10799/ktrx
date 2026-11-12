package timeline

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const revisionAnnotation = "deployment.kubernetes.io/revision"

type Analyzer struct {
	Client kubernetes.Interface
	Now    func() time.Time
}

func (a *Analyzer) AnalyzeDeployment(ctx context.Context, namespace, name string) (*Report, error) {
	if a.Client == nil {
		return nil, fmt.Errorf("Kubernetes client is required")
	}
	deployment, err := a.Client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	if deployment.Spec.Selector == nil { return nil, fmt.Errorf("deployment %s/%s has no selector", namespace, name) }
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("deployment has invalid selector: %w", err)
	}
	replicaSets, err := a.Client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, fmt.Errorf("list ReplicaSets for deployment %s/%s: %w", namespace, name, err)
	}

	owned := make([]appsv1.ReplicaSet, 0, len(replicaSets.Items))
	for _, rs := range replicaSets.Items {
		for _, owner := range rs.OwnerReferences {
			if owner.Controller != nil && *owner.Controller && owner.UID == deployment.UID && owner.Kind == "Deployment" {
				owned = append(owned, rs)
				break
			}
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		ri, _ := strconv.Atoi(owned[i].Annotations[revisionAnnotation])
		rj, _ := strconv.Atoi(owned[j].Annotations[revisionAnnotation])
		if ri != rj {
			return ri < rj
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
			"Environment variable values are never included; changed variables are identified by name only.",
			"This timeline reports observed changes and does not establish causation.",
		},
	}
	currentRevision := deployment.Annotations[revisionAnnotation]
	var previous *appsv1.ReplicaSet
	var previousCreation time.Time
	var lastConditionUpdate time.Time
	for _, condition := range deployment.Status.Conditions {
		if condition.LastUpdateTime.Time.After(lastConditionUpdate) { lastConditionUpdate = condition.LastUpdateTime.Time }
	}
	for i := range owned {
		current := &owned[i]
		revision := current.Annotations[revisionAnnotation]
		if revision == "" {
			revision = "unknown"
			report.Limitations = appendUnique(report.Limitations, "One or more ReplicaSets lack the revision annotation; their ordering uses creation timestamps.")
		}
		event := TimelineEvent{
			Timestamp: current.CreationTimestamp.Time,
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
				event.Details = "Revision order conflicts with ReplicaSet creation time: older ReplicaSet reused (rollback likely). Exact rollback time is unavailable."
				report.Limitations = appendUnique(report.Limitations, "Rollback reused an older ReplicaSet; its creation timestamp is historical, not the rollback time.")
				if lastConditionUpdate.After(previousCreation) && !lastConditionUpdate.After(now) { event.Timestamp = lastConditionUpdate; event.Details += " Deployment condition lastUpdateTime is an approximation, not an audit timestamp." } else { event.Timestamp = time.Time{} }
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
	return report, nil
}


func appendUnique(items []string, value string) []string {
	for _, item := range items { if item == value { return items } }
	return append(items, value)
}
