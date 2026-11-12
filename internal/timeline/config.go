package timeline

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
)

type configReference struct {
	kind     string
	name     string
	required bool
}

var configMapGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

func addConfigResourceEvents(ctx context.Context, client metadata.Interface, report *Report, replicaSets []appsv1.ReplicaSet, currentRevision string, since time.Time) {
	refs := map[string]configReference{}
	add := func(kind, name string, required bool) {
		if name == "" { return }
		key := kind + "/" + name
		ref := refs[key]
		ref.kind, ref.name = kind, name
		ref.required = ref.required || required
		refs[key] = ref
	}
	for _, rs := range replicaSets {
		if currentRevision != "" && rs.Annotations[revisionAnnotation] != currentRevision { continue }
		podSpec := rs.Spec.Template.Spec
		for _, volume := range podSpec.Volumes {
			if volume.ConfigMap != nil { add("ConfigMap", volume.ConfigMap.Name, !optional(volume.ConfigMap.Optional)) }
			if volume.Secret != nil { add("Secret", volume.Secret.SecretName, !optional(volume.Secret.Optional)) }
			if volume.Projected != nil {
				for _, source := range volume.Projected.Sources {
					if source.ConfigMap != nil { add("ConfigMap", source.ConfigMap.Name, !optional(source.ConfigMap.Optional)) }
					if source.Secret != nil { add("Secret", source.Secret.Name, !optional(source.Secret.Optional)) }
				}
			}
		}
		containers := append(append([]corev1.Container{}, podSpec.InitContainers...), podSpec.Containers...)
		for _, container := range containers {
			for _, env := range container.Env {
				if env.ValueFrom == nil { continue }
				if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil { add("ConfigMap", ref.Name, !optional(ref.Optional)) }
				if ref := env.ValueFrom.SecretKeyRef; ref != nil { add("Secret", ref.Name, !optional(ref.Optional)) }
			}
			for _, source := range container.EnvFrom {
				if source.ConfigMapRef != nil { add("ConfigMap", source.ConfigMapRef.Name, !optional(source.ConfigMapRef.Optional)) }
				if source.SecretRef != nil { add("Secret", source.SecretRef.Name, !optional(source.SecretRef.Optional)) }
			}
		}
	}
	if client == nil {
		report.Limitations = appendUnique(report.Limitations, "ConfigMap/Secret metadata was not queried because a metadata-only Kubernetes client was unavailable.")
		return
	}
	keys := make([]string, 0, len(refs))
	for key := range refs { keys = append(keys, key) }
	sort.Strings(keys)
	for _, key := range keys {
		ref := refs[key]
		gvr := configMapGVR
		if ref.kind == "Secret" { gvr = secretGVR }
		object, err := client.Resource(gvr).Namespace(report.Namespace).Get(ctx, ref.name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) && ref.required {
				failureAt := timePointer(report.GeneratedAt)
				report.Events = append(report.Events, TimelineEvent{
					Timestamp: failureAt, Type: EventFailure,
					Resource: ref.kind + "/" + ref.name,
					Action: "Required referenced configuration resource is missing",
					Source: "metadata-api", Confidence: "observed",
					Details: fmt.Sprintf("A required %s referenced by a retained Pod template was not found; values were not requested.", ref.kind),
				})
			} else if !apierrors.IsNotFound(err) {
				report.Limitations = appendUnique(report.Limitations, fmt.Sprintf("Could not read metadata for referenced %s %s (RBAC or API error); no resource data was requested.", ref.kind, ref.name))
			}
			continue
		}
		latestTime := latestManagedFieldsTime(object.ManagedFields)
		if latestTime == nil { latestTime = timePointer(object.CreationTimestamp.Time) }
		if latestTime == nil || latestTime.Before(since) { continue }
		managers := map[string]struct{}{}
		for _, entry := range object.ManagedFields {
			if entry.Manager != "" { managers[entry.Manager] = struct{}{} }
		}
		managerNames := make([]string, 0, len(managers))
		for manager := range managers { managerNames = append(managerNames, manager) }
		sort.Strings(managerNames)
		details := "resourceVersion=" + object.ResourceVersion
		if len(managerNames) > 0 { details += " managers=" + strings.Join(managerNames, ",") }
		details += " latestManagedFieldsTime=" + latestTime.UTC().Format(time.RFC3339)
		report.Events = append(report.Events, TimelineEvent{
			Timestamp: latestTime, Type: EventConfig, Resource: ref.kind + "/" + ref.name,
			Action: "Referenced configuration metadata changed within the selected time window",
			Source: "metadata-only resourceVersion+managedFields", Confidence: "metadata-only",
			Details: details + " (resource values are never requested or displayed)",
		})
	}
}

func optional(value *bool) bool { return value != nil && *value }

func latestManagedFieldsTime(fields []metav1.ManagedFieldsEntry) *time.Time {
	var latest time.Time
	for _, entry := range fields {
		if entry.Time != nil && entry.Time.After(latest) { latest = entry.Time.Time }
	}
	return timePointer(latest)
}
