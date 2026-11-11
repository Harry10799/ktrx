package analyzer

import (
	"context"
	"fmt"
	"sort"

	networkingv1 "k8s.io/api/networking/v1"
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
}

type ingressBackend struct {
	service string
	port    string
}

func traceIngress(ctx context.Context, client kubernetes.Interface, namespace string, services []ServiceTrace) ([]IngressTrace, []Evidence) {
	if len(services) == 0 {
		return nil, nil
	}
	serviceNames := make(map[string]struct{}, len(services))
	for _, svc := range services {
		serviceNames[svc.Name] = struct{}{}
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
	for i := range ingresses.Items {
		ing := &ingresses.Items[i]
		className := ""
		if ing.Spec.IngressClassName != nil {
			className = *ing.Spec.IngressClassName
		}
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			backend := ingressServiceBackend(ing.Spec.DefaultBackend.Service)
			if _, ok := serviceNames[backend.service]; ok {
				routes = append(routes, IngressTrace{
					Name: ing.Name, Class: className, Host: "*",
					Path: "/", PathType: "DefaultBackend",
					Service: backend.service, ServicePort: backend.port,
				})
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
	if len(routes) == 0 {
		return nil, []Evidence{{
			Check: "ingress-routes", Verdict: Healthy,
			Summary: "No Ingress routes reference Services selecting this pod",
		}}
	}
	evidence := make([]Evidence, 0, len(routes))
	for _, route := range routes {
		evidence = append(evidence, Evidence{
			Check: "ingress-route/" + route.Name,
			Verdict: Healthy,
			Summary: fmt.Sprintf("Ingress %s routes host %s path %s to Service %s:%s", route.Name, route.Host, route.Path, route.Service, route.ServicePort),
			Details: []string{"class=" + valueOrUnknown(route.Class), "pathType=" + valueOrUnknown(route.PathType)},
		})
	}
	return routes, evidence
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
