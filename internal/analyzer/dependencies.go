package analyzer

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type DependencyTrace struct {
	Service      string   `json:"service" yaml:"service"`
	Namespace    string   `json:"namespace" yaml:"namespace"`
	References   []string `json:"references,omitempty" yaml:"references,omitempty"`
	SelectedPods int      `json:"selectedPods" yaml:"selectedPods"`
	ReadyPods    int      `json:"readyPods" yaml:"readyPods"`
	UnreadyPods  []string `json:"unreadyPods,omitempty" yaml:"unreadyPods,omitempty"`
	Verdict      Verdict  `json:"verdict" yaml:"verdict"`
	Summary      string   `json:"summary" yaml:"summary"`
}

var serviceHostKey = regexp.MustCompile(`(?i)(HOST|URL|URI|SERVICE|ENDPOINT|DATABASE|DB)`)

// traceDependencies follows explicit literal references to same-namespace
// Services only. It never fetches Secret/ConfigMap values or scans other namespaces.
func traceDependencies(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) ([]DependencyTrace, []Evidence) {
	services, err := client.CoreV1().Services(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, []Evidence{{Check: "dependency-discovery", Verdict: Unknown,
			Summary: "Could not list namespace Services for direct dependency correlation",
			Details: []string{err.Error()}, Suggestion: "Grant list access to Services in the source Pod namespace."}}
	}
	serviceByName := make(map[string]*corev1.Service, len(services.Items))
	for i := range services.Items { serviceByName[services.Items[i].Name] = &services.Items[i] }
	references := map[string][]string{}
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Value == "" { continue }
			for _, name := range referencedServices(env.Value, env.Name, pod.Namespace, serviceByName) {
				references[name] = appendUnique(references[name], container.Name+"/"+env.Name)
			}
		}
		for _, arg := range append(append([]string{}, container.Command...), container.Args...) {
			for _, name := range referencedServices(arg, "COMMAND", pod.Namespace, serviceByName) {
				references[name] = appendUnique(references[name], container.Name+"/command")
			}
		}
	}
	if len(references) == 0 {
		return nil, []Evidence{{Check: "dependency-discovery", Verdict: Info,
			Summary: "No same-namespace Service dependencies found in literal container environment values or commands",
			Suggestion: "Expose dependency hostnames through explicit variables such as DB_HOST or API_URL. Values stored only in Secrets/ConfigMaps are intentionally not read."}}
	}
	names := make([]string, 0, len(references))
	for name := range references { names = append(names, name) }
	sort.Strings(names)
	limit := len(names)
	if limit > 20 { limit = 20 }
	traces := make([]DependencyTrace, 0, limit)
	evidence := make([]Evidence, 0, limit+1)
	for _, name := range names[:limit] {
		svc := serviceByName[name]
		trace := DependencyTrace{Service: name, Namespace: pod.Namespace, References: references[name]}
		if len(svc.Spec.Selector) == 0 {
			trace.Verdict = Unknown
			trace.Summary = "Referenced Service has no Pod selector; backend health cannot be inferred from selected Pods"
			evidence = append(evidence, Evidence{Check: "dependency/" + name, Verdict: Unknown, Summary: trace.Summary,
				Details: trace.References, Suggestion: "Inspect manually managed Endpoints/EndpointSlices or the external service behind this Service."})
			traces = append(traces, trace)
			continue
		}
		selector := labels.Set(svc.Spec.Selector).AsSelector().String()
		pods, listErr := client.CoreV1().Pods(pod.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if listErr != nil {
			trace.Verdict = Unknown
			trace.Summary = "Could not inspect Pods selected by the referenced Service"
			evidence = append(evidence, Evidence{Check: "dependency/" + name, Verdict: Unknown, Summary: trace.Summary,
				Details: append([]string{listErr.Error()}, trace.References...), Suggestion: "Grant list access to Pods in the source namespace and retry."})
			traces = append(traces, trace)
			continue
		}
		trace.SelectedPods = len(pods.Items)
		for i := range pods.Items {
			target := &pods.Items[i]
			if podReady(target) { trace.ReadyPods++ } else {
				trace.UnreadyPods = append(trace.UnreadyPods, target.Name+" ("+string(target.Status.Phase)+")")
			}
		}
		switch {
		case trace.SelectedPods == 0:
			trace.Verdict, trace.Summary = Failed, "Referenced Service selector matches no Pods"
			evidence = append(evidence, Evidence{Check: "dependency/" + name, Verdict: Failed, Summary: trace.Summary,
				Details: trace.References, Suggestion: "Check the Service selector and labels/namespace of its intended backend Pods."})
		case trace.ReadyPods == 0:
			trace.Verdict = Failed
			trace.Summary = fmt.Sprintf("No selected backend Pods are Ready (%d selected)", trace.SelectedPods)
			details := append(append([]string{}, trace.References...), trace.UnreadyPods...)
			evidence = append(evidence, Evidence{Check: "dependency/" + name, Verdict: Failed, Summary: trace.Summary,
				Details: details, Suggestion: "Inspect the dependency Pods' readiness, Events and logs. This identifies an unhealthy backend but does not prove it caused the source Pod's symptoms."})
		case trace.ReadyPods < trace.SelectedPods:
			trace.Verdict = Warning
			trace.Summary = fmt.Sprintf("Referenced Service has partial backend readiness (%d/%d Ready)", trace.ReadyPods, trace.SelectedPods)
			details := append(append([]string{}, trace.References...), trace.UnreadyPods...)
			evidence = append(evidence, Evidence{Check: "dependency/" + name, Verdict: Warning, Summary: trace.Summary,
				Details: details, Suggestion: "Inspect unready backend Pods and confirm whether reduced capacity affects this workload."})
		default:
			trace.Verdict = Healthy
			trace.Summary = fmt.Sprintf("All selected backend Pods are Ready (%d/%d)", trace.ReadyPods, trace.SelectedPods)
			evidence = append(evidence, Evidence{Check: "dependency/" + name, Verdict: Healthy, Summary: trace.Summary, Details: trace.References})
		}
		traces = append(traces, trace)
	}
	if len(names) > limit {
		evidence = append(evidence, Evidence{Check: "dependency-discovery-limit", Verdict: Info,
			Summary: fmt.Sprintf("Inspection is capped at 20 Services; %d additional references were not inspected", len(names)-limit)})
	}
	return traces, evidence
}

func referencedServices(raw, key, namespace string, services map[string]*corev1.Service) []string {
	value := strings.TrimSpace(raw)
	if value == "" { return nil }
	hosts := []string{}
	if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" { hosts = append(hosts, parsed.Hostname()) }
	host := value
	if h, _, err := net.SplitHostPort(value); err == nil { host = h
	} else if strings.Count(value, ":") == 1 && !strings.Contains(value, "://") {
		parts := strings.SplitN(value, ":", 2)
		if _, err := strconv.Atoi(parts[1]); err == nil { host = parts[0] }
	}
	if !strings.Contains(host, "/") && !strings.Contains(host, " ") { hosts = append(hosts, strings.TrimSuffix(host, ".")) }
	var found []string
	for _, candidate := range hosts {
		parts := strings.Split(candidate, ".")
		name := ""
		switch {
		case len(parts) >= 3 && parts[1] == namespace && parts[2] == "svc":
			name = parts[0]
		case len(parts) >= 2 && parts[1] == "svc":
			name = parts[0]
		case len(parts) == 1 && serviceHostKey.MatchString(key):
			name = parts[0]
		}
		if _, ok := services[name]; ok { found = appendUnique(found, name) }
	}
	return found
}

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning { return false }
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady { return condition.Status == corev1.ConditionTrue }
	}
	return false
}
