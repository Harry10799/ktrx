package analyzer

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type PodAnalyzer struct {
	Client kubernetes.Interface
}

func (a *PodAnalyzer) Analyze(ctx context.Context, namespace, name string) (*PodReport, error) {
	pod, err := a.Client.CoreV1().Pods(namespace).Get(ctx, name, metav1GetOptions())
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("pod %s/%s not found", namespace, name)
		}
		return nil, fmt.Errorf("get pod: %w", err)
	}

	report := &PodReport{
		Namespace: pod.Namespace, Name: pod.Name, Phase: string(pod.Status.Phase),
		Node: pod.Spec.NodeName, PodIP: pod.Status.PodIP, QOSClass: string(pod.Status.QOSClass),
		Age: time.Since(pod.CreationTimestamp.Time).Round(time.Second).String(),
		ServiceAccount: pod.Spec.ServiceAccountName, Labels: pod.Labels, Verdict: Healthy,
	}
	report.OwnerChain = a.ownerChain(ctx, pod)
	readyCount := 0
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			readyCount++
		}
	}
	report.Ready = readyCount == 1

	statusByName := map[string]corev1.ContainerStatus{}
	for _, status := range pod.Status.ContainerStatuses {
		statusByName[status.Name] = status
		report.Restarts += status.RestartCount
	}
	for _, container := range pod.Spec.Containers {
		status := statusByName[container.Name]
		info := ContainerInfo{Name: container.Name, Image: container.Image, Ready: status.Ready, RestartCount: status.RestartCount}
		if status.State.Running != nil { info.State = "Running" }
		if status.State.Waiting != nil { info.State, info.Reason = "Waiting", status.State.Waiting.Reason }
		if status.State.Terminated != nil { info.State, info.Reason = "Terminated", status.State.Terminated.Reason }
		info.Requests = quantities(container.Resources.Requests)
		info.Limits = quantities(container.Resources.Limits)
		report.Containers = append(report.Containers, info)
	}

	switch {
	case pod.DeletionTimestamp != nil:
		report.Evidence = append(report.Evidence, Evidence{Check:"pod-lifecycle", Verdict:Unknown, Summary:"Pod is terminating"})
	case pod.Status.Phase == corev1.PodFailed:
		report.Evidence = append(report.Evidence, Evidence{Check:"pod-phase", Verdict:Failed, Summary:"Pod phase is Failed", Suggestion:"Inspect container termination reasons and namespace events."})
	case pod.Status.Phase == corev1.PodPending:
		report.Evidence = append(report.Evidence, Evidence{Check:"pod-phase", Verdict:Unknown, Summary:"Pod is Pending", Suggestion:"Inspect scheduling events, image pulls and volume mounts."})
	default:
		report.Evidence = append(report.Evidence, Evidence{Check:"pod-phase", Verdict:Healthy, Summary:"Pod phase is " + string(pod.Status.Phase)})
	}
	if !report.Ready {
		report.Evidence = append(report.Evidence, Evidence{Check:"pod-readiness", Verdict:Failed, Summary:"Pod Ready condition is not True", Suggestion:"Inspect readiness probe configuration and container logs."})
	} else {
		report.Evidence = append(report.Evidence, Evidence{Check:"pod-readiness", Verdict:Healthy, Summary:"Pod Ready condition is True"})
	}
	if report.Restarts > 0 {
		report.Evidence = append(report.Evidence, Evidence{Check:"container-restarts", Verdict:Unknown, Summary:fmt.Sprintf("Containers have restarted %d time(s)", report.Restarts), Suggestion:"Inspect previous container logs and pod events for OOMKilled, probe failures or process exits."})
	}
	report.Verdict = aggregate(report.Evidence)
	return report, nil
}

func (a *PodAnalyzer) ownerChain(ctx context.Context, pod *corev1.Pod) []string {
	chain := []string{}
	owner := controllerOwner(pod.OwnerReferences)
	if owner == nil { return chain }
	kind, name := owner.Kind, owner.Name
	for depth := 0; depth < 3 && name != ""; depth++ {
		chain = append(chain, kind+"/"+name)
		switch kind {
		case "ReplicaSet":
			rs, err := a.Client.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, name, metav1GetOptions())
			if err != nil { return chain }
			owner = controllerOwner(rs.OwnerReferences)
		case "Deployment":
			return chain
		default:
			return chain
		}
		if owner == nil { break }
		kind, name = owner.Kind, owner.Name
	}
	return chain
}

func controllerOwner(refs []metav1OwnerReference) *metav1OwnerReference {
	for i := range refs { if refs[i].Controller != nil && *refs[i].Controller { return &refs[i] } }
	return nil
}

func quantities(values corev1.ResourceList) map[string]string {
	if len(values) == 0 { return nil }
	out := map[string]string{}
	for name, value := range values { out[string(name)] = value.String() }
	return out
}

func aggregate(items []Evidence) Verdict {
	result := Healthy
	for _, item := range items {
		if item.Verdict == Failed { return Failed }
		if item.Verdict == Unknown { result = Unknown }
	}
	return result
}

var _ = strings.TrimSpace
var _ resource.Quantity
var _ types.UID
