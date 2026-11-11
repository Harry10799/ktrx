package analyzer

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// IngressTrace describes an Ingress rule that routes to a Service selecting
// the pod being inspected. It does not claim that the external load balancer
// or DNS is reachable.
type IngressTrace struct {
	Name        string   `json:"name" yaml:"name"`
	Class       string   `json:"class,omitempty" yaml:"class,omitempty"`
	Host        string   `json:"host" yaml:"host"`
	Path        string   `json:"path" yaml:"path"`
	PathType    string   `json:"pathType,omitempty" yaml:"pathType,omitempty"`
	Service     string   `json:"service" yaml:"service"`
	ServicePort string   `json:"servicePort" yaml:"servicePort"`
	ClassExists *bool `json:"classExists,omitempty" yaml:"classExists,omitempty"`
	ControllerPods int `json:"controllerPods,omitempty" yaml:"controllerPods,omitempty"`
	ControllerReady int `json:"controllerReady,omitempty" yaml:"controllerReady,omitempty"`
	Addresses []string `json:"addresses,omitempty" yaml:"addresses,omitempty"`
}

type ingressBackend struct {
	service string
	port    string
}

func traceIngress(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, services []ServiceTrace) ([]IngressTrace, []Evidence) {
	namespace := pod.Namespace
	serviceNames := make(map[string]struct{}, len(services))
	for _, svc := range services {
		serviceNames[svc.Name] = struct{}{}
	}

	namespaceServices, serviceErr := client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if serviceErr != nil {
		return nil, []Evidence{{
			Check: "ingress-backends", Verdict: Unknown,
			Summary: "Could not verify whether Ingress backend Services exist",
			Details: []string{serviceErr.Error()},
		}}
	}
	existingServices := make(map[string]struct{}, len(namespaceServices.Items))
	for i := range namespaceServices.Items {
		existingServices[namespaceServices.Items[i].Name] = struct{}{}
	}

	ingresses, err := client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, []Evidence{{
			Check: "ingress-routes", Verdict: Unknown,
			Summary: "Could not list Ingress resources in the pod namespace",
			Details: []string{err.Error()},
			Suggestion: "Grant read access to networking.k8s.io Ingress resources in the namespace.",
		}}
	}

	var routes []IngressTrace
	var evidence []Evidence
	missingBackends := make(map[string]struct{})
	namespaceNotes := make(map[string]struct{})
	relatedNames := []string{pod.Name}
	for _, owner := range pod.OwnerReferences {
		relatedNames = append(relatedNames, owner.Name)
	}
	checkBackend := func(ingressName, serviceName string) bool {
		if _, exists := existingServices[serviceName]; !exists {
			key := ingressName + "/" + serviceName
			related := false
			for _, name := range relatedNames {
				if serviceNameRelated(serviceName, name) { related = true; break }
			}
			if related { missingBackends[key] = struct{}{} } else { namespaceNotes[key] = struct{}{} }
			return false
		}
		return true
	}
	for i := range ingresses.Items {
		ing := &ingresses.Items[i]
		className := ""
		if ing.Spec.IngressClassName != nil {
			className = *ing.Spec.IngressClassName
		}
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			backend := ingressServiceBackend(ing.Spec.DefaultBackend.Service)
			if checkBackend(ing.Name, backend.service) {
				if _, ok := serviceNames[backend.service]; ok {
					routes = append(routes, IngressTrace{
					Name: ing.Name, Class: className, Host: "*",
					Path: "/", PathType: "DefaultBackend",
					Service: backend.service, ServicePort: backend.port,
					})
				}
			}
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			host := rule.Host
			if host == "" {
				host = "*"
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service == nil {
					continue
				}
				backend := ingressServiceBackend(path.Backend.Service)
				if !checkBackend(ing.Name, backend.service) {
					continue
				}
				if _, ok := serviceNames[backend.service]; !ok {
					continue
				}
				pathValue, pathType := path.Path, ""
				if pathValue == "" {
					pathValue = "/"
				}
				if path.PathType != nil {
					pathType = string(*path.PathType)
				}
				routes = append(routes, IngressTrace{
					Name: ing.Name, Class: className, Host: host, Path: pathValue,
					PathType: pathType, Service: backend.service, ServicePort: backend.port,
				})
			}
		}
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Name != routes[j].Name { return routes[i].Name < routes[j].Name }
		if routes[i].Host != routes[j].Host { return routes[i].Host < routes[j].Host }
		return routes[i].Path < routes[j].Path
	})
	missingKeys := make([]string, 0, len(missingBackends))
	for key := range missingBackends { missingKeys = append(missingKeys, key) }
	sort.Strings(missingKeys)
	for _, key := range missingKeys {
		evidence = append(evidence, Evidence{
			Check: "ingress-backend/" + key, Verdict: Failed,
			Summary: "Ingress references a backend Service that does not exist in namespace " + namespace,
			Details: []string{key},
			Suggestion: "Create the referenced Service or correct the Ingress backend service name.",
		})
	}
	if len(namespaceNotes) > 0 {
		noteKeys := make([]string, 0, len(namespaceNotes))
		for key := range namespaceNotes { noteKeys = append(noteKeys, key) }
		sort.Strings(noteKeys)
		evidence = append(evidence, Evidence{
			Check: "ingress-namespace-note", Verdict: Info,
			Summary: "Unrelated Ingress resources reference missing backend Services; they were not counted against this Pod",
			Details: noteKeys,
		})
	}
	if len(routes) == 0 {
		if len(missingBackends) == 0 {
			evidence = append(evidence, Evidence{
				Check: "ingress-routes", Verdict: Info,
				Summary: "No Ingress routes reference Services selecting this pod; no route relationship was established",
			})
		}
		return nil, evidence
	}
	checked := map[string]bool{}
	for i := range routes {
		route := &routes[i]
		if !checked[route.Name] {
			checked[route.Name] = true
			ing, getErr := client.NetworkingV1().Ingresses(namespace).Get(ctx, route.Name, metav1.GetOptions{})
			if getErr != nil {
				evidence = append(evidence, Evidence{Check: "ingress-status/" + route.Name, Verdict: Unknown, Summary: "Could not inspect Ingress status", Details: []string{getErr.Error()}})
			} else {
				for _, item := range ing.Status.LoadBalancer.Ingress {
					if item.IP != "" { route.Addresses = append(route.Addresses, item.IP) }
					if item.Hostname != "" { route.Addresses = append(route.Addresses, item.Hostname) }
				}
				if route.Class == "" {
					evidence = append(evidence, Evidence{Check: "ingress-class/" + route.Name, Verdict: Unknown, Summary: "Ingress does not specify ingressClassName; default class resolution was not verified"})
				} else if _, classErr := client.NetworkingV1().IngressClasses().Get(ctx, route.Class, metav1.GetOptions{}); classErr != nil {
					if apierrors.IsNotFound(classErr) {
						route.ClassExists = boolPointer(false)
						evidence = append(evidence, Evidence{Check: "ingress-class/" + route.Name, Verdict: Failed, Summary: "Referenced IngressClass " + route.Class + " does not exist", Details: []string{classErr.Error()}, Suggestion: "Create the IngressClass or correct spec.ingressClassName."})
					} else {
						evidence = append(evidence, Evidence{Check: "ingress-class/" + route.Name, Verdict: Unknown, Summary: "Could not verify referenced IngressClass", Details: []string{classErr.Error()}})
					}
				} else {
					route.ClassExists = boolPointer(true)
					evidence = append(evidence, Evidence{Check: "ingress-class/" + route.Name, Verdict: Healthy, Summary: "IngressClass " + route.Class + " exists"})
				}
				if len(route.Addresses) == 0 {
					evidence = append(evidence, Evidence{Check: "ingress-address/" + route.Name, Verdict: Unknown, Summary: "Ingress has no load-balancer address assigned", Suggestion: "Check the Ingress controller and its Service; some local controllers do not publish an address."})
				} else {
					evidence = append(evidence, Evidence{Check: "ingress-address/" + route.Name, Verdict: Healthy, Summary: "Ingress has an assigned load-balancer address", Details: route.Addresses})
				}
				controllers, listErr := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=controller"})
				if listErr != nil {
					evidence = append(evidence, Evidence{Check: "ingress-controller/" + route.Name, Verdict: Unknown, Summary: "Could not inspect Ingress controller pods", Details: []string{listErr.Error()}})
				} else {
					route.ControllerPods = len(controllers.Items)
					for _, controller := range controllers.Items {
						if controller.Status.Phase == corev1.PodRunning && podReady(controller) { route.ControllerReady++ }
					}
					if route.ControllerPods == 0 {
						evidence = append(evidence, Evidence{Check: "ingress-controller/" + route.Name, Verdict: Unknown, Summary: "No controller pods found with the standard controller label; controller discovery may be implementation-specific"})
					} else if route.ControllerReady == 0 {
						evidence = append(evidence, Evidence{Check: "ingress-controller/" + route.Name, Verdict: Failed, Summary: "Ingress controller pods were found but none are Ready"})
					} else {
						evidence = append(evidence, Evidence{Check: "ingress-controller/" + route.Name, Verdict: Healthy, Summary: fmt.Sprintf("%d/%d discovered Ingress controller pods are Ready", route.ControllerReady, route.ControllerPods)})
					}
				}
			}
		}
		evidence = append(evidence, Evidence{
			Check: "ingress-route/" + route.Name,
			Verdict: Healthy,
			Summary: fmt.Sprintf("Ingress %s routes host %s path %s to Service %s:%s", route.Name, route.Host, route.Path, route.Service, route.ServicePort),
			Details: []string{"class=" + valueOrUnknown(route.Class), "pathType=" + valueOrUnknown(route.PathType)},
		})
	}
	return routes, evidence
}

func promoteRoutedSelectorMismatches(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, evidence []Evidence) {
	ingresses, err := client.NetworkingV1().Ingresses(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	backends := make(map[string]struct{})
	for i := range ingresses.Items {
		ing := &ingresses.Items[i]
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			backends[ing.Spec.DefaultBackend.Service.Name] = struct{}{}
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil { continue }
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil { backends[path.Backend.Service.Name] = struct{}{} }
			}
		}
	}
	for i := range evidence {
		if evidence[i].Check != "inbound-service-selector" { continue }
		for _, detail := range evidence[i].Details {
			for serviceName := range backends {
				if strings.HasPrefix(detail, "Service "+serviceName+" selector mismatch:") {
					evidence[i].Verdict = Failed
					evidence[i].Summary = "An Ingress-routed Service has a selector mismatch and currently has no endpoints"
					evidence[i].Suggestion = "Fix the Service selector or Pod labels immediately; this Service is referenced by an Ingress route and cannot select this Pod."
					return
				}
			}
		}
	}
}

func boolPointer(v bool) *bool { return &v }

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady { return condition.Status == corev1.ConditionTrue }
	}
	return false
}

func ingressServiceBackend(backend *networkingv1.IngressServiceBackend) ingressBackend {
	port := ""
	if backend.Port.Name != "" {
		port = backend.Port.Name
	} else if backend.Port.Number != 0 {
		port = fmt.Sprintf("%d", backend.Port.Number)
	} else {
		port = "unspecified"
	}
	return ingressBackend{service: backend.Name, port: port}
}

func valueOrUnknown(v string) string {
	if v == "" { return "unknown" }
	return v
}
