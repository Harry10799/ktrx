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
	kustomizations, kustomErr := listAcrossNamespaces(ctx, client, []string{report.Namespace, "flux-system"}, fluxKustomizationResources, report)
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
			sourceNamespace, _, _ := unstructured.NestedString(object.Object, "spec", "sourceRef", "namespace")
			if sourceNamespace == "" { sourceNamespace = object.GetNamespace() }
			timestamp := fluxKustomizationTime(object)
			details := "sourceRef=" + sourceKind + "/" + sourceName
			if revision != "" { details += " revision=" + revision }
			if hash := commitHash(revision); hash != "" {
				details += " commit=" + hash
				if commitURL := gitCommitURL(ctx, client, sourceNamespace, sourceKind, sourceName, hash); commitURL != "" {
					details += " commitURL=" + commitURL
				}
			}
			report.Events = append(report.Events, TimelineEvent{
				Timestamp: timestamp, Type: EventGitOps,
				Resource: "Kustomization/" + object.GetName(),
				Action: "Flux Kustomization reconciliation state observed",
				Source: "flux-kustomization-status", Confidence: "controller-status",
				Details: details,
			})
		}
	}
	helmReleases, helmErr := listAcrossNamespaces(ctx, client, []string{report.Namespace, "flux-system"}, fluxHelmReleaseResources, report)
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
	addArgoCDHistory(ctx, client, report, labels)
}

func listAcrossNamespaces(ctx context.Context, client dynamic.Interface, namespaces []string, resources []schema.GroupVersionResource, report *Report) ([]unstructured.Unstructured, error) {
	combined := make([]unstructured.Unstructured, 0)
	successes := 0
	var lastErr error
	seen := map[string]struct{}{}
	for _, namespace := range namespaces {
		if _, exists := seen[namespace]; exists { continue }
		seen[namespace] = struct{}{}
		items, err := listFirstAvailable(ctx, client, namespace, resources)
		if err != nil {
			lastErr = err
			report.Limitations = appendUnique(report.Limitations, "Could not list optional GitOps resources in namespace "+namespace+" (CRD missing or RBAC denied).")
			continue
		}
		successes++
		combined = append(combined, items...)
	}
	if successes == 0 { return nil, lastErr }
	return combined, nil
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
		if fluxInventoryMatchesDeployment(id, namespace, deploymentName) { return true }
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

func fluxKustomizationTime(object unstructured.Unstructured) *time.Time {
	// lastHandledReconcileAt is the controller's reconciliation timestamp. Do not
	// substitute an old Ready condition transition time: it can remain unchanged
	// across many later reconciliations and look like a stale deployment event.
	lastApplied, _, _ := unstructured.NestedString(object.Object, "status", "lastAppliedRevision")
	lastAttempted, _, _ := unstructured.NestedString(object.Object, "status", "lastAttemptedRevision")
	if lastApplied == "" || (lastAttempted != "" && lastApplied != lastAttempted) {
		return nil
	}
	value, _, _ := unstructured.NestedString(object.Object, "status", "lastHandledReconcileAt")
	if value == "" { value, _, _ = unstructured.NestedString(object.Object, "status", "lastAppliedAt") }
	return parseHistoryTime(value)
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
	index := strings.LastIndex(revision, "sha1:")
	if index < 0 { return "" }
	hash := revision[index+len("sha1:"):]
	if len(hash) != 40 { return "" }
	for _, char := range hash {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) { return "" }
	}
	return hash
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
	case "bitbucket.org":
		return "https://bitbucket.org" + repositoryPath + "/commits/" + hash
	default:
		return ""
	}
}

func fluxInventoryMatchesDeployment(id, namespace, name string) bool {
	id = strings.Trim(id, "/")
	parts := strings.Split(id, "_")
	if len(parts) >= 4 {
		// Flux inventory IDs use namespace_name_group_kind.
		return parts[0] == namespace && parts[1] == name && strings.EqualFold(parts[len(parts)-1], "Deployment")
	}
	parts = strings.Split(id, "/")
	if len(parts) == 3 {
		return parts[0] == "Deployment" && parts[1] == namespace && parts[2] == name
	}
	return false
}

func addArgoCDHistory(ctx context.Context, client dynamic.Interface, report *Report, labels map[string]string) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	namespaces := []string{"argocd"}
	if report.Namespace != "argocd" { namespaces = append(namespaces, report.Namespace) }
	seen := map[string]struct{}{}
	foundAPI := false
	for _, namespace := range namespaces {
		applications, err := client.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil { continue }
		foundAPI = true
		for _, app := range applications.Items {
			if _, ok := seen[app.GetNamespace()+"/"+app.GetName()]; ok { continue }
			if !argoApplicationTargetsDeployment(app, report.Namespace, report.Target, labels) { continue }
			seen[app.GetNamespace()+"/"+app.GetName()] = struct{}{}
			repoURL, _, _ := unstructured.NestedString(app.Object, "spec", "source", "repoURL")
			history, _, _ := unstructured.NestedSlice(app.Object, "status", "history")
			if len(history) == 0 {
				revision, _, _ := unstructured.NestedString(app.Object, "status", "sync", "revision")
				appendArgoHistoryEvent(report, app, revision, latestConditionTime(app), repoURL, "current sync status")
				continue
			}
			for _, raw := range history {
				entry, ok := raw.(map[string]interface{})
				if !ok { continue }
				revision := nestedString(entry, "revision")
				timestamp := parseHistoryTime(nestedString(entry, "deployedAt"))
				details := "historyID=" + fmt.Sprint(entry["id"])
				if source, ok := entry["source"].(map[string]interface{}); ok {
					if targetRevision := nestedString(source, "targetRevision"); targetRevision != "" { details += " targetRevision=" + targetRevision }
				}
				appendArgoHistoryEvent(report, app, revision, timestamp, repoURL, details)
			}
		}
	}
	if !foundAPI {
		report.Limitations = appendUnique(report.Limitations, "Argo CD Application history is unavailable (Application CRD missing or list permission denied in argocd/workload namespaces).")
	}
}

func argoApplicationTargetsDeployment(app unstructured.Unstructured, namespace, target string, labels map[string]string) bool {
	appName := app.GetName()
	appNamespace := app.GetNamespace()
	instance := labels["argocd.argoproj.io/instance"]
	if instance == appName || instance == appNamespace+"_"+appName || instance == appNamespace+"/"+appName { return true }
	deploymentName := strings.TrimPrefix(target, "Deployment/")
	resources, _, _ := unstructured.NestedSlice(app.Object, "status", "resources")
	for _, raw := range resources {
		resource, ok := raw.(map[string]interface{})
		if !ok { continue }
		if nestedString(resource, "kind") == "Deployment" &&
			nestedString(resource, "namespace") == namespace &&
			nestedString(resource, "name") == deploymentName {
			group := nestedString(resource, "group")
			if group == "" || group == "apps" { return true }
		}
	}
	return false
}

func appendArgoHistoryEvent(report *Report, app unstructured.Unstructured, revision string, timestamp *time.Time, repoURL, details string) {
	if revision != "" { details += " revision=" + revision }
	if hash := normalizeCommitHash(revision); hash != "" {
		details += " commit=" + hash
		if link := gitRepositoryCommitURL(repoURL, hash); link != "" { details += " commitURL=" + link }
	}
	report.Events = append(report.Events, TimelineEvent{
		Timestamp: timestamp, Type: EventGitOps,
		Resource: "ArgoCD Application/" + app.GetNamespace() + "/" + app.GetName(),
		Revision: revision, Action: "Argo CD deployment history observed",
		Source: "argocd-application-history", Confidence: "controller-status",
		Details: details,
	})
}

func normalizeCommitHash(revision string) string {
	hash := commitHash(revision)
	if hash != "" { return hash }
	value := strings.TrimSpace(revision)
	if len(value) == 40 {
		for _, char := range value {
			if !strings.ContainsRune("0123456789abcdefABCDEF", char) { return "" }
		}
		return value
	}
	return ""
}

func gitRepositoryCommitURL(repositoryURL, hash string) string {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Host == "" || hash == "" { return "" }
	repositoryPath := strings.TrimSuffix(parsed.Path, ".git")
	switch strings.ToLower(parsed.Host) {
	case "github.com":
		return "https://github.com" + repositoryPath + "/commit/" + hash
	case "gitlab.com":
		return "https://gitlab.com" + repositoryPath + "/-/commit/" + hash
	case "bitbucket.org":
		return "https://bitbucket.org" + repositoryPath + "/commits/" + hash
	default:
		return ""
	}
}
