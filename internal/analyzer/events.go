package analyzer

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func collectPodEvents(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) ([]EventInfo, *Evidence) {
	selector := "involvedObject.kind=Pod,involvedObject.name=" + pod.Name
	list, err := client.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil {
		return nil, &Evidence{
			Check: "pod-events", Verdict: Unknown,
			Summary: "Could not retrieve events for this pod",
			Details: []string{err.Error()},
			Suggestion: "Grant read access to Events in the pod namespace.",
		}
	}
	items := make([]EventInfo, 0, len(list.Items))
	for _, event := range list.Items {
		at := event.EventTime.Time
		if at.IsZero() { at = event.LastTimestamp.Time }
		if at.IsZero() { at = event.CreationTimestamp.Time }
		items = append(items, EventInfo{
			Type: event.Type, Reason: event.Reason, Message: event.Message,
			Count: event.Count, Timestamp: at.UTC().Format(time.RFC3339),
			Source: event.ReportingController,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Timestamp < items[j].Timestamp })
	return items, nil
}
