package timeline

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
)

func compareTemplates(before, after *appsv1.ReplicaSet) []Change {
	var changes []Change
	left, right := containerIndex(before.Spec.Template.Spec.Containers), containerIndex(after.Spec.Template.Spec.Containers)
	names := make(map[string]struct{}, len(left)+len(right))
	for name := range left { names[name] = struct{}{} }
	for name := range right { names[name] = struct{}{} }
	ordered := make([]string, 0, len(names))
	for name := range names { ordered = append(ordered, name) }
	sort.Strings(ordered)
	for _, name := range ordered {
		old, oldOK := left[name]
		current, newOK := right[name]
		if !oldOK {
			changes = append(changes, Change{Field: "containers." + name, Before: "<absent>", After: "present"})
			continue
		}
		if !newOK {
			changes = append(changes, Change{Field: "containers." + name, Before: "present", After: "<absent>"})
			continue
		}
		addChange(&changes, "containers."+name+".image", old.Image, current.Image)
		compareResources(&changes, name, "requests", old.Resources.Requests, current.Resources.Requests)
		compareResources(&changes, name, "limits", old.Resources.Limits, current.Resources.Limits)
		compareEnv(&changes, name, old.Env, current.Env)
		compareEnvFrom(&changes, name, old.EnvFrom, current.EnvFrom)
	}
	return changes
}

func containerIndex(items []corev1.Container) map[string]corev1.Container {
	out := make(map[string]corev1.Container, len(items))
	for _, item := range items { out[item.Name] = item }
	return out
}

func addChange(changes *[]Change, field, before, after string) {
	if before != after {
		*changes = append(*changes, Change{Field: field, Before: before, After: after})
	}
}

func compareResources(changes *[]Change, container, kind string, before, after corev1.ResourceList) {
	keys := map[string]struct{}{}
	for key := range before { keys[string(key)] = struct{}{} }
	for key := range after { keys[string(key)] = struct{}{} }
	ordered := make([]string, 0, len(keys))
	for key := range keys { ordered = append(ordered, key) }
	sort.Strings(ordered)
	for _, key := range ordered {
		old, oldOK := before[corev1.ResourceName(key)]
		current, newOK := after[corev1.ResourceName(key)]
		oldValue, newValue := "<unset>", "<unset>"
		if oldOK { oldValue = old.String() }
		if newOK { newValue = current.String() }
		addChange(changes, fmt.Sprintf("containers.%s.resources.%s.%s", container, kind, key), oldValue, newValue)
	}
}

func compareEnv(changes *[]Change, container string, before, after []corev1.EnvVar) {
	left, right := envIndex(before), envIndex(after)
	keys := map[string]struct{}{}
	for key := range left { keys[key] = struct{}{} }
	for key := range right { keys[key] = struct{}{} }
	ordered := make([]string, 0, len(keys))
	for key := range keys { ordered = append(ordered, key) }
	sort.Strings(ordered)
	for _, key := range ordered {
		old, oldOK := left[key]
		current, newOK := right[key]
		field := "containers." + container + ".env." + key
		switch {
		case !oldOK:
			*changes = append(*changes, Change{Field: field, Before: "<absent>", After: "<present>"})
		case !newOK:
			*changes = append(*changes, Change{Field: field, Before: "<present>", After: "<absent>"})
		case !reflect.DeepEqual(old, current):
			*changes = append(*changes, Change{Field: field, Before: "<redacted>", After: "<redacted>"})
		}
	}
}

func envIndex(items []corev1.EnvVar) map[string]corev1.EnvVar {
	out := make(map[string]corev1.EnvVar, len(items))
	for _, item := range items { out[item.Name] = item }
	return out
}

func compareEnvFrom(changes *[]Change, container string, before, after []corev1.EnvFromSource) {
	if reflect.DeepEqual(before, after) { return }
	format := func(items []corev1.EnvFromSource) string {
		values := make([]string, 0, len(items))
		for _, item := range items {
			switch {
			case item.ConfigMapRef != nil:
				values = append(values, "configMap/"+item.ConfigMapRef.Name)
			case item.SecretRef != nil:
				values = append(values, "secret/"+item.SecretRef.Name)
			}
		}
		sort.Strings(values)
		return strings.Join(values, ",")
	}
	addChange(changes, "containers."+container+".envFrom", format(before), format(after))
}
