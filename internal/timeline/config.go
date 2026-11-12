package timeline

import (
	"context"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type configReference struct {
	kind string
	name string
}

func addConfigResourceEvents(ctx context.Context, client kubernetes.Interface, report *Report, replicaSets []appsv1.ReplicaSet) {
	refs := map[string]configReference{}
	add := func(kind, name string) {
		if name == "" { return }
		key := kind + "/" + name
		refs[key] = configReference{kind: kind, name: name}
	}
	for _, rs := range replicaSets {
		podSpec := rs.Spec.Template.Spec
		for _, volume := range podSpec.Volumes {
			if volume.ConfigMap != nil { add("ConfigMap", volume.ConfigMap.Name) }
			if volume.Secret != nil { add("Secret", volume.Secret.SecretName) }
			if volume.Projected != nil {
				for _, source := range volume.Projected.Sources {
					if source.ConfigMap != nil { add("ConfigMap", source.ConfigMap.Name) }
					if source.Secret != nil { add("Secret", source.Secret.Name) }
				}
			}
		}
		containers := append(append([]corev1.Container{}, podSpec.InitContainers...), podSpec.Containers...)
		for _, container := range containers {
			for _, env := range container.Env {
				if env.ValueFrom == nil { continue }
				if env.ValueFrom.ConfigMapKeyRef != nil { add("ConfigMap", env.ValueFrom.ConfigMapKeyRef.Name) }
				if env.ValueFrom.SecretKeyRef != nil { add("Secret", env.ValueFrom.SecretKeyRef.Name) }
			}
			for _, source := range container.EnvFrom {
				if source.ConfigMapRef != nil { add("ConfigMap", source.ConfigMapRef.Name) }
				if source.SecretRef != nil { add("Secret", source.SecretRef.Name) }
			}
		}
	}
	keys := make([]string, 0, len(refs))
	for key := range refs { keys = append(keys, key) }
	sort.Strings(keys)
	for _, key := range keys {
		ref := refs[key]
		var fields []metav1.ManagedFieldsEntry
		var resourceVersion string
		switch ref.kind {
		case "ConfigMap":
			object, err := client.CoreV1().ConfigMaps(report.Namespace).Get(ctx, ref.name, metav1.GetOptions{})
			if err != nil { continue }
			fields, resourceVersion = object.ManagedFields, object.ResourceVersion
		case "Secret":
			object, err := client.CoreV1().Secrets(report.Namespace).Get(ctx, ref.name, metav1.GetOptions{})
			if err != nil { continue }
			fields, resourceVersion = object.ManagedFields, object.ResourceVersion
		}
		var latestTime *metav1.Time
		managers := map[string]struct{}{}
		for _, entry := range fields {
			if entry.Manager != "" { managers[entry.Manager] = struct{}{} }
			if entry.Time != nil && (latestTime == nil || entry.Time.After(latestTime.Time)) {
				latestTime = entry.Time
			}
		}
		managerNames := make([]string, 0, len(managers))
		for manager := range managers { managerNames = append(managerNames, manager) }
		sort.Strings(managerNames)
		var timestamp *time.Time
		if latestTime != nil { timestamp = timePointer(latestTime.Time) }
		details := "resourceVersion=" + resourceVersion
		if len(managerNames) > 0 { details += " managers=" + strings.Join(managerNames, ",") }
		if timestamp != nil { details += " latestManagedFieldsTime=" + timestamp.UTC().Format(time.RFC3339) }
		report.Events = append(report.Events, TimelineEvent{
			Timestamp: timestamp, Type: EventConfig, Resource: ref.kind + "/" + ref.name,
			Action: "Referenced configuration metadata observed (values are never read or displayed)",
			Source: "resourceVersion+managedFields", Confidence: "metadata-only", Details: details,
		})
	}
}
