package analyzer

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func collectPodEvents(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, verbose bool) ([]EventInfo, *Evidence) {
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
	sort.Slice(list.Items, func(i, j int) bool { return eventTime(list.Items[i]).Before(eventTime(list.Items[j])) })
	for _, event := range list.Items {
		if !verbose && event.Type != corev1.EventTypeWarning { continue }
		at := event.EventTime.Time
		if at.IsZero() { at = event.LastTimestamp.Time }
		if at.IsZero() { at = event.CreationTimestamp.Time }
		items = append(items, EventInfo{
			Type: event.Type, Reason: event.Reason, Message: event.Message,
			Count: event.Count, Timestamp: relativeTime(at),
			Source: event.ReportingController,
		})
	}
	return items, nil
}

func eventTime(e corev1.Event) time.Time {
	if !e.EventTime.IsZero() { return e.EventTime.Time }
	if !e.LastTimestamp.IsZero() { return e.LastTimestamp.Time }
	return e.CreationTimestamp.Time
}

func relativeTime(t time.Time) string {
	if t.IsZero() { return "unknown" }
	d := time.Since(t)
	if d < 0 { d = 0 }
	switch {
	case d < time.Minute: return "just now"
	case d < time.Hour: return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour: return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour: return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default: return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
