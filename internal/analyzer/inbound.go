package analyzer

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// ServiceTrace describes one Service whose selector matches the traced pod.
type ServiceTrace struct {
	Name              string            `json:"name" yaml:"name"`
	Type              string            `json:"type" yaml:"type"`
	ClusterIP         string            `json:"clusterIP,omitempty" yaml:"clusterIP,omitempty"`
	Selector          map[string]string `json:"selector,omitempty" yaml:"selector,omitempty"`
	Ports             []string          `json:"ports,omitempty" yaml:"ports,omitempty"`
	EndpointSource    string            `json:"endpointSource,omitempty" yaml:"endpointSource,omitempty"`
	EndpointFound     bool              `json:"endpointFound" yaml:"endpointFound"`
	EndpointReady     bool              `json:"endpointReady" yaml:"endpointReady"`
	EndpointReadyKnown bool             `json:"endpointReadyKnown" yaml:"endpointReadyKnown"`
	EndpointAddresses []string          `json:"endpointAddresses,omitempty" yaml:"endpointAddresses,omitempty"`
}

// traceInbound discovers Services selecting the pod and verifies that the pod
// is published as one of their backends. API permission failures are evidence,
// not fatal errors, so the rest of the pod report remains available.
func traceInbound(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) ([]ServiceTrace, []Evidence) {
	services, err := client.CoreV1().Services(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, []Evidence{{
			Check: "inbound-services", Verdict: Unknown,
			Summary: "Could not list Services in the pod namespace",
			Details: []string{err.Error()},
			Suggestion: "Grant read access to Services in the namespace and retry.",
		}}
	}

	var traces []ServiceTrace
	var evidence []Evidence
	podLabels := labels.Set(pod.Labels)
	for i := range services.Items {
		svc := &services.Items[i]
		if len(svc.Spec.Selector) == 0 || !labels.Set(svc.Spec.Selector).AsSelector().Matches(podLabels) {
			continue
		}

		trace := ServiceTrace{
			Name: svc.Name, Type: string(svc.Spec.Type), ClusterIP: svc.Spec.ClusterIP,
			Selector: svc.Spec.Selector, Ports: servicePortDescriptions(svc.Spec.Ports),
		}
		sliceFound, sliceReady, sliceReadyKnown, sliceAddresses, sliceErr := findPodInEndpointSlices(ctx, client, svc, pod)
		if sliceFound {
			trace.EndpointSource = "EndpointSlice"
			trace.EndpointFound = true
			trace.EndpointReady = sliceReady
			trace.EndpointReadyKnown = sliceReadyKnown
			trace.EndpointAddresses = sliceAddresses
		} else {
			legacyFound, legacyReady, legacyAddresses, legacyErr := findPodInEndpoints(ctx, client, svc, pod)
			if legacyFound {
				trace.EndpointSource = "Endpoints"
				trace.EndpointFound = true
				trace.EndpointReady = legacyReady
				trace.EndpointReadyKnown = true
				trace.EndpointAddresses = legacyAddresses
			} else if sliceErr != nil && legacyErr != nil {
				evidence = append(evidence, Evidence{
					Check: "inbound-endpoint/" + svc.Name, Verdict: Unknown,
					Summary: "Could not inspect EndpointSlices or Endpoints for Service " + svc.Name,
					Details: []string{sliceErr.Error(), legacyErr.Error()},
					Suggestion: "Grant read access to discovery.k8s.io EndpointSlices and core Endpoints.",
				})
				traces = append(traces, trace)
				continue
			}
		}

		switch {
		case !trace.EndpointFound:
			evidence = append(evidence, Evidence{
				Check: "inbound-endpoint/" + svc.Name, Verdict: Failed,
				Summary: fmt.Sprintf("Service %s selects this pod, but the pod is not present in its endpoints", svc.Name),
				Suggestion: "Check the Service selector, pod labels, readiness and EndpointSlice/Endpoints controller state.",
			})
		case trace.EndpointReadyKnown && !trace.EndpointReady:
			evidence = append(evidence, Evidence{
				Check: "inbound-endpoint/" + svc.Name, Verdict: Failed,
				Summary: fmt.Sprintf("Pod is registered in Service %s endpoints but is not ready", svc.Name),
				Details: trace.EndpointAddresses,
				Suggestion: "Inspect pod readiness conditions and readiness probes.",
			})
		case !trace.EndpointReadyKnown:
			evidence = append(evidence, Evidence{
				Check: "inbound-endpoint/" + svc.Name, Verdict: Unknown,
				Summary: fmt.Sprintf("Pod is registered in Service %s EndpointSlice, but endpoint readiness is unknown", svc.Name),
				Details: trace.EndpointAddresses,
			})
		default:
			evidence = append(evidence, Evidence{
				Check: "inbound-endpoint/" + svc.Name, Verdict: Healthy,
				Summary: fmt.Sprintf("Pod is a ready backend for Service %s", svc.Name),
				Details: trace.EndpointAddresses,
			})
		}
		traces = append(traces, trace)
	}

	if len(traces) == 0 {
		evidence = append(evidence, Evidence{
			Check: "inbound-services", Verdict: Healthy,
			Summary: "No selector-based Services in this namespace select the pod",
		})
	}
	sort.Slice(traces, func(i, j int) bool { return traces[i].Name < traces[j].Name })
	return traces, evidence
}

func findPodInEndpointSlices(ctx context.Context, client kubernetes.Interface, svc *corev1.Service, pod *corev1.Pod) (bool, bool, bool, []string, error) {
	selector := labels.Set{"kubernetes.io/service-name": svc.Name}.AsSelector().String()
	slices, err := client.DiscoveryV1().EndpointSlices(svc.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, false, false, nil, err
	}
	found, ready, readyKnown := false, false, true
	var addresses []string
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.TargetRef == nil || endpoint.TargetRef.Kind != "Pod" || endpoint.TargetRef.Name != pod.Name {
				continue
			}
			if endpoint.TargetRef.UID != "" && pod.UID != "" && endpoint.TargetRef.UID != pod.UID {
				continue
			}
			found = true
			if endpoint.Conditions.Ready == nil {
				readyKnown = false
			} else if *endpoint.Conditions.Ready {
				ready = true
			}
			addresses = append(addresses, endpoint.Addresses...)
		}
	}
	return found, ready, readyKnown, uniqueSorted(addresses), nil
}

func findPodInEndpoints(ctx context.Context, client kubernetes.Interface, svc *corev1.Service, pod *corev1.Pod) (bool, bool, []string, error) {
	endpoints, err := client.CoreV1().Endpoints(svc.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		return false, false, nil, err
	}
	var addresses []string
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			if address.TargetRef != nil && address.TargetRef.Kind == "Pod" && address.TargetRef.Name == pod.Name {
				addresses = append(addresses, address.IP)
			}
		}
		for _, address := range subset.NotReadyAddresses {
			if address.TargetRef != nil && address.TargetRef.Kind == "Pod" && address.TargetRef.Name == pod.Name {
				addresses = append(addresses, address.IP)
			}
		}
	}
	if len(addresses) == 0 {
		return false, false, nil, nil
	}
	ready := false
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			if address.TargetRef != nil && address.TargetRef.Kind == "Pod" && address.TargetRef.Name == pod.Name {
				ready = true
			}
		}
	}
	return true, ready, uniqueSorted(addresses), nil
}

func servicePortDescriptions(ports []corev1.ServicePort) []string {
	result := make([]string, 0, len(ports))
	for _, port := range ports {
		target := port.TargetPort.String()
		if port.TargetPort.IntVal == 0 && port.TargetPort.StrVal == "" {
			target = fmt.Sprintf("%d", port.Port)
		}
		name := port.Name
		if name == "" {
			name = fmt.Sprintf("%d", port.Port)
		}
		item := fmt.Sprintf("%s -> %s/%s", name, target, port.Protocol)
		result = append(result, item)
	}
	return result
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok { continue }
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

