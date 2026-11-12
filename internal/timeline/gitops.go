package timeline

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var fluxKustomizationResources = []schema.GroupVersionResource{
	{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"},
	{Group: "kustomize.toolkit.fluxcd.io", Version: "v1beta2", Resource: "kustomizations"},
}

var fluxHelmReleaseResources = []schema.GroupVersionResource{
	{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
	{Group: "helm.toolkit.fluxcd.io", Version: "v2beta2", Resource: "helmreleases"},
	{Group: "helm.toolkit.fluxcd.io", Version: "v2beta1", Resource: "helmreleases"},
}

func addGitOpsEvents(ctx context.Context, client dynamic.Interface, report *Report, labels map[string]string) {
	if client == nil {
		report.Limitations = appendUnique(report.Limitations, "Flux history was not queried because a dynamic Kubernetes client was unavailable.")
		return
	}
	kustomizations, kustomErr := listFirstAvailable(ctx, client, report.Namespace, fluxKustomizationResources)
	if kustomErr != nil {
		report.Limitations = appendUnique(report.Limitations, "Flux Kustomization history is unavailable (CRD missing or RBAC denied).")
	} else {
		for _, object := range kustomizations {
			if !kustomizationTargetsDeployment(object, report.Namespace, report.Target, labels) { continue }
			revision, _, _ := unstructured.NestedString(object.Object, "status", "lastAppliedRevision")
			if revision == "" {
				revision, _, _ = unstructured.NestedString(object.Object, "status", "lastAttemptedRevision")
			}
			sourceKind, _, _ := unstructured.NestedString(object.Object, "spec", "sourceRef", "kind")
			sourceName, _, _ := unstructured.NestedString(object.Object, "spec", "sourceRef", "name")
			timestamp := latestConditionTime(object)
			details := "sourceRef=" + sourceKind + "/" + sourceName
			if revision != "" { details += " revision=" + revision }
			if hash := commitHash(revision); hash != "" {
				details += " commit=" + hash
				if commitURL := gitCommitURL(ctx, client, report.Namespace, sourceKind, sourceName, hash); commitURL != "" {
					details += " commitURL=" + commitURL
				}
			}
			report.Events = append(report.Events, TimelineEvent{
				Timestamp: timestamp, Type: EventGitOps,
				Resource: "Kustomization/" + object.GetName(),
				Action: "Flux reconciliation history observed",
				Source: "flux-kustomization-status", Confidence: "controller-status",
				Details: details,
			})
		}
	}
	helmReleases, helmErr := listFirstAvailable(ctx, client, report.Namespace, fluxHelmReleaseResources)
	if helmErr != nil {
		report.Limitations = appendUnique(report.Limitations, "Flux HelmRelease history is unavailable (CRD missing or RBAC denied).")
	} else {
		for _, object := range helmReleases {
			if !helmReleaseTargetsDeployment(object, labels) { continue }
			history, _, _ := unstructured.NestedSlice(object.Object, "status", "history")
			if len(history) == 0 {
				revision, _, _ := unstructured.NestedInt64(object.Object, "status", "lastAttemptedRevision")
				report.Events = append(report.Events, TimelineEvent{
					Timestamp: latestConditionTime(object), Type: EventGitOps,
					Resource: "HelmRelease/" + object.GetName(),
					Action: "Flux HelmRelease status observed",
					Source: "flux-helmrelease-status", Confidence: "controller-status",
					Details: fmt.Sprintf("lastAttemptedRevision=%d", revision),
				})
				continue
			}
			for _, raw := range history {
				entry, ok := raw.(map[string]interface{})
				if !ok { continue }
				version := nestedString(entry, "version")
				chart := nestedString(entry, "chartName")
				chartVersion := nestedString(entry, "chartVersion")
				appVersion := nestedString(entry, "appVersion")
				status := nestedString(entry, "status")
				digest := nestedString(entry, "digest")
				configDigest := nestedString(entry, "configDigest")
				timestamp := parseHistoryTime(nestedString(entry, "lastDeployed"))
				details := "chart=" + chart + " chartVersion=" + chartVersion + " appVersion=" + appVersion + " status=" + status
				if digest != "" { details += " digest=" + digest }
				if configDigest != "" { details += " configDigest=" + configDigest }
				report.Events = append(report.Events, TimelineEvent{
					Timestamp: timestamp, Type: EventGitOps,
					Resource: "HelmRelease/" + object.GetName(),
					Revision: version, Action: "Helm release history entry",
					Source: "flux-helmrelease-history", Confidence: "controller-status",
					Details: strings.TrimSpace(details),
				})
			}
		}
	}
}

func listFirstAvailable(ctx context.Context, client dynamic.Interface, namespace string, resources []schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	var lastErr error
	for _, resource := range resources {
		list, err := client.Resource(resource).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err == nil { return list.Items, nil }
		lastErr = err
	}
	return nil, lastErr
}

func kustomizationTargetsDeployment(object unstructured.Unstructured, namespace, target string, labels map[string]string) bool {
	name := labels["kustomize.toolkit.fluxcd.io/name"]
	labelNamespace := labels["kustomize.toolkit.fluxcd.io/namespace"]
	if name != "" && object.GetName() == name && (labelNamespace == "" || labelNamespace == object.GetNamespace()) { return true }
	entries, _, _ := unstructured.NestedSlice(object.Object, "status", "inventory", "entries")
	deploymentName := strings.TrimPrefix(target, "Deployment/")
	for _, raw := range entries {
		entry, ok := raw.(map[string]interface{})
		if !ok { continue }
		id := nestedString(entry, "id")
		if id == "Deployment/"+namespace+"/"+deploymentName || id == "Deployment_"+namespace+"_"+deploymentName { return true }
	}
	return false
}

func helmReleaseTargetsDeployment(object unstructured.Unstructured, labels map[string]string) bool {
	instance := labels["app.kubernetes.io/instance"]
	releaseName := labels["helm.toolkit.fluxcd.io/name"]
	if instance != "" && object.GetName() == instance { return true }
	if releaseName != "" && object.GetName() == releaseName { return true }
	return false
}

func latestConditionTime(object unstructured.Unstructured) *time.Time {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	var latest time.Time
	for _, raw := range conditions {
		entry, ok := raw.(map[string]interface{})
		if !ok { continue }
		value := nestedString(entry, "lastTransitionTime")
		parsed := parseHistoryTime(value)
		if parsed != nil && parsed.After(latest) { latest = *parsed }
	}
	if latest.IsZero() { return nil }
	return &latest
}

func parseHistoryTime(value string) *time.Time {
	if value == "" { return nil }
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil { return nil }
	return &parsed
}

func nestedString(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return value
}

func commitHash(revision string) string {
	for _, marker := range []string{"sha1:", "sha256:"} {
		if index := strings.LastIndex(revision, marker); index >= 0 {
			return revision[index+len(marker):]
		}
	}
	return ""
}

func gitCommitURL(ctx context.Context, client dynamic.Interface, namespace, kind, name, hash string) string {
	if client == nil || !strings.EqualFold(kind, "GitRepository") || name == "" || hash == "" {
		return ""
	}
	resources := []schema.GroupVersionResource{
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
		{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "gitrepositories"},
	}
	var source *unstructured.Unstructured
	for _, resource := range resources {
		object, err := client.Resource(resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			source = object
			break
		}
	}
	if source == nil {
		return ""
	}
	repositoryURL, _, _ := unstructured.NestedString(source.Object, "spec", "url")
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	repositoryPath := strings.TrimSuffix(parsed.Path, ".git")
	switch strings.ToLower(parsed.Host) {
	case "github.com":
		return "https://github.com" + repositoryPath + "/commit/" + hash
	case "gitlab.com":
		return "https://gitlab.com" + repositoryPath + "/-/commit/" + hash
	default:
		return ""
	}
}
