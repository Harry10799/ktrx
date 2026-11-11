package analyzer

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PodAnalyzer struct {
	Client kubernetes.Interface
	IncludeLogs bool
	VerboseEvents bool
}

func (a *PodAnalyzer) Analyze(ctx context.Context, namespace, name string) (*PodReport, error) {
	pod, err := a.Client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("pod %s/%s not found", namespace, name)
		}
		return nil, fmt.Errorf("get pod: %w", err)
	}

	report := &PodReport{
		Namespace: pod.Namespace, Name: pod.Name, Phase: string(pod.Status.Phase),
		Node: pod.Spec.NodeName, PodIP: pod.Status.PodIP, QOSClass: string(pod.Status.QOSClass),
		Age: relativeTime(pod.CreationTimestamp.Time),
		ServiceAccount: pod.Spec.ServiceAccountName, Labels: pod.Labels, Verdict: Healthy,
	}
	report.OwnerChain = a.ownerChain(ctx, pod)
	dependencies, dependencyEvidence := traceDependencies(ctx, a.Client, pod)
	report.Dependencies = dependencies
	report.Evidence = append(report.Evidence, dependencyEvidence...)
	inboundServices, inboundEvidence := traceInbound(ctx, a.Client, pod)
	report.InboundServices = inboundServices
	report.Evidence = append(report.Evidence, inboundEvidence...)
	ingressRoutes, ingressEvidence := traceIngress(ctx, a.Client, pod.Namespace, inboundServices)
	report.IngressRoutes = ingressRoutes
	report.Evidence = append(report.Evidence, ingressEvidence...)
	events, eventEvidence := collectPodEvents(ctx, a.Client, pod, a.VerboseEvents)
	report.Events = events
	if eventEvidence != nil { report.Evidence = append(report.Evidence, *eventEvidence) }
	logs, logEvidence := collectContainerLogs(ctx, a.Client, pod, a.IncludeLogs)
	report.Logs = logs
	report.Evidence = append(report.Evidence, logEvidence...)
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			report.Ready = true
			break
		}
	}

	statusByName := map[string]corev1.ContainerStatus{}
	for _, status := range pod.Status.ContainerStatuses {
		statusByName[status.Name] = status
		report.Restarts += status.RestartCount
	}
	for _, container := range pod.Spec.Containers {
		status := statusByName[container.Name]
		info := ContainerInfo{
			Name: container.Name, Image: container.Image, Ready: status.Ready,
			RestartCount: status.RestartCount, Requests: quantities(container.Resources.Requests),
			Limits: quantities(container.Resources.Limits),
		}
		switch {
		case status.State.Running != nil:
			info.State = "Running"
		case status.State.Waiting != nil:
			info.State, info.Reason = "Waiting", status.State.Waiting.Reason
		case status.State.Terminated != nil:
			info.State, info.Reason = "Terminated", status.State.Terminated.Reason
		default:
			info.State = "Unknown"
		}
		report.Containers = append(report.Containers, info)
	}

	switch {
	case pod.DeletionTimestamp != nil:
		report.Evidence = append(report.Evidence, Evidence{Check: "pod-lifecycle", Verdict: Unknown, Summary: "Pod is terminating"})
	case pod.Status.Phase == corev1.PodFailed:
		report.Evidence = append(report.Evidence, Evidence{Check: "pod-phase", Verdict: Failed, Summary: "Pod phase is Failed", Suggestion: "Inspect container termination reasons and namespace events."})
	case pod.Status.Phase == corev1.PodPending:
		report.Evidence = append(report.Evidence, Evidence{Check: "pod-phase", Verdict: Unknown, Summary: "Pod is Pending", Suggestion: "Inspect scheduling events, image pulls and volume mounts."})
	default:
		report.Evidence = append(report.Evidence, Evidence{Check: "pod-phase", Verdict: Healthy, Summary: "Pod phase is " + string(pod.Status.Phase)})
	}
	if !report.Ready {
		report.Evidence = append(report.Evidence, Evidence{Check: "pod-readiness", Verdict: Failed, Summary: "Pod Ready condition is not True", Suggestion: "Inspect readiness probe configuration and container logs."})
	} else {
		report.Evidence = append(report.Evidence, Evidence{Check: "pod-readiness", Verdict: Healthy, Summary: "Pod Ready condition is True"})
	}
	if report.Restarts > 0 {
		report.Evidence = append(report.Evidence, Evidence{Check: "container-restarts", Verdict: Unknown, Summary: fmt.Sprintf("Containers have restarted %d time(s)", report.Restarts), Suggestion: "Inspect previous container logs and pod events for OOMKilled, probe failures or process exits."})
	}

	// Group probe recommendations to avoid inflating warning counts.
	for _, container := range pod.Spec.Containers {
		if container.ReadinessProbe == nil {
			report.Evidence = append(report.Evidence, Evidence{Check: "probes/" + container.Name, Verdict: Warning, Summary: "No readiness probe configured", Suggestion: "Configure readiness checks so Services only send traffic when the application is ready."})
		} else if report.Ready {
			report.Evidence = append(report.Evidence, Evidence{Check: "probe/readiness/" + container.Name, Verdict: Healthy, Summary: "Readiness probe is configured and Pod Ready condition is True"})
		} else {
			report.Evidence = append(report.Evidence, Evidence{Check: "probe/readiness/" + container.Name, Verdict: Failed, Summary: "Readiness probe is configured but Pod Ready condition is not True", Suggestion: "Inspect probe path, port, thresholds and container logs."})
		}
		var optionalMissing []string
		if container.LivenessProbe == nil { optionalMissing = append(optionalMissing, "liveness") } else {
			report.Evidence = append(report.Evidence, Evidence{Check: "probe/liveness/" + container.Name, Verdict: Unknown, Summary: "Liveness probe is configured; current execution result is not directly exposed in Pod status"})
		}
		if container.StartupProbe == nil { optionalMissing = append(optionalMissing, "startup") } else {
			report.Evidence = append(report.Evidence, Evidence{Check: "probe/startup/" + container.Name, Verdict: Unknown, Summary: "Startup probe is configured; current execution result is not directly exposed in Pod status"})
		}
		if len(optionalMissing) > 0 { report.Evidence = append(report.Evidence, Evidence{Check: "probes-optional/" + container.Name, Verdict: Info, Summary: "No " + strings.Join(optionalMissing, "/") + " probe configured; add only when required by the application"}) }
		if imageUsesMutableTag(container.Image) { report.Evidence = append(report.Evidence, Evidence{Check: "image-tag/" + container.Name, Verdict: Warning, Summary: "Container image uses a mutable or implicit tag: " + container.Image, Suggestion: "Pin the image to an immutable digest or a controlled version tag."}) }
	}
	if pod.Status.QOSClass == corev1.PodQOSBestEffort {
		report.Evidence = append(report.Evidence, Evidence{Check: "resource-qos", Verdict: Warning, Summary: "Pod QoS is BestEffort; containers have no CPU or memory requests/limits", Suggestion: "Define appropriate resource requests and limits to improve scheduling and resource isolation."})
	}
	outbound, outboundEvidence := traceOutbound(ctx, a.Client, pod)
	report.Outbound = outbound
	report.Evidence = append(report.Evidence, outboundEvidence...)

	checkServicePortChain(pod, report)
	report.WarningCount, report.UnknownCount, report.FailedCount = countVerdicts(report.Evidence)
	report.Verdict = aggregate(report.Evidence)
	return report, nil
}

func (a *PodAnalyzer) ownerChain(ctx context.Context, pod *corev1.Pod) []string {
	chain := []string{}
	owner := controllerOwner(pod.OwnerReferences)
	if owner == nil {
		return chain
	}
	kind, name := owner.Kind, owner.Name
	for depth := 0; depth < 3 && name != ""; depth++ {
		chain = append(chain, kind+"/"+name)
		if kind != "ReplicaSet" {
			break
		}
		rs, err := a.Client.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			break
		}
		owner = controllerOwner(rs.OwnerReferences)
		if owner == nil {
			break
		}
		kind, name = owner.Kind, owner.Name
	}
	return chain
}

func controllerOwner(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

func quantities(values corev1.ResourceList) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := map[string]string{}
	for name, value := range values {
		out[string(name)] = value.String()
	}
	return out
}

func imageUsesMutableTag(image string) bool {
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon <= lastSlash { return true }
	tag := image[lastColon+1:]
	return tag == "" || tag == "latest"
}

func aggregate(items []Evidence) Verdict {
	// Explicit precedence: FAILED > WARN > UNKNOWN > HEALTHY.
	result := Healthy
	for _, item := range items {
		switch item.Verdict {
		case Failed:
			return Failed
		case Warning:
			result = Warning
		case Unknown:
			if result != Warning { result = Unknown }
		}
	}
	return result
}

func countVerdicts(items []Evidence) (warnings, unknown, failed int) {
	for _, item := range items {
		switch item.Verdict {
		case Warning: warnings++
		case Unknown: unknown++
		case Failed: failed++
		}
	}
	return
}

// Port-chain diagnostics compare Service targetPorts with declared container
// ports, then compare HTTP/TCP probe ports with the same declarations.
func checkServicePortChain(pod *corev1.Pod, report *PodReport) {
	for _, svc := range report.InboundServices {
		for _, sp := range svc.Ports {
			parts := strings.SplitN(sp, " -> ", 2)
			if len(parts) != 2 { continue }
			target := strings.SplitN(parts[1], "/", 2)[0]
			var targetPort int32
			_, scanErr := fmt.Sscanf(target, "%d", &targetPort)
			numeric := scanErr == nil
			matched := false
			for _, container := range pod.Spec.Containers {
				for _, cp := range container.Ports {
					if (numeric && cp.ContainerPort == targetPort) || (!numeric && cp.Name == target) {
						matched = true
						targetPort = cp.ContainerPort
					}
				}
			}
			if matched {
				report.Evidence = append(report.Evidence, Evidence{Check: "port-chain/" + svc.Name, Verdict: Healthy, Summary: fmt.Sprintf("Service %s targetPort %q resolves to declared containerPort %d", svc.Name, target, targetPort)})
			} else {
				declared := false
				for _, container := range pod.Spec.Containers {
					if len(container.Ports) > 0 { declared = true; break }
				}
				verdict := Unknown
				summary := fmt.Sprintf("Service %s targetPort %q has no matching declared containerPort; the application may still listen on it", svc.Name, target)
				if declared {
					verdict = Warning
					summary = fmt.Sprintf("Service %s targetPort %q does not match any declared containerPort; verify the application listener and Service mapping", svc.Name, target)
				}
				report.Evidence = append(report.Evidence, Evidence{Check: "port-chain/" + svc.Name, Verdict: verdict, Summary: summary, Suggestion: "Verify Service targetPort against the port the application actually listens on."})
			}
		}
	}
	for _, container := range pod.Spec.Containers {
		probes := []struct{name string; probe *corev1.Probe}{
			{"readiness", container.ReadinessProbe},
			{"liveness", container.LivenessProbe},
			{"startup", container.StartupProbe},
		}
		for _, item := range probes {
			port, portName, set := probePort(item.probe)
			if !set { continue }
			matched := false
			for _, cp := range container.Ports {
				if (portName == "" && cp.ContainerPort == port) || (portName != "" && cp.Name == portName) { matched = true; port = cp.ContainerPort }
			}
			if matched {
				report.Evidence = append(report.Evidence, Evidence{Check: "probe-port/" + container.Name + "/" + item.name, Verdict: Healthy, Summary: fmt.Sprintf("%s probe port resolves to declared containerPort %d", item.name, port)})
			} else {
				label := portName
				if label == "" { label = fmt.Sprintf("%d", port) }
				report.Evidence = append(report.Evidence, Evidence{Check: "probe-port/" + container.Name + "/" + item.name, Verdict: Unknown, Summary: fmt.Sprintf("%s probe port %q has no matching declared containerPort; the process may still listen on it", item.name, label)})
			}
		}
	}
}

func probePort(probe *corev1.Probe) (int32, string, bool) {
	if probe == nil { return 0, "", false }
	switch {
	case probe.HTTPGet != nil:
		if probe.HTTPGet.Port.Type == 0 { return probe.HTTPGet.Port.IntVal, "", true }
		return 0, probe.HTTPGet.Port.StrVal, true
	case probe.TCPSocket != nil:
		if probe.TCPSocket.Port.Type == 0 { return probe.TCPSocket.Port.IntVal, "", true }
		return 0, probe.TCPSocket.Port.StrVal, true
	case probe.GRPC != nil:
		return probe.GRPC.Port, "", true
	default:
		return 0, "", false
	}
}
