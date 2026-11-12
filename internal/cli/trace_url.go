package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/spf13/cobra"
)

type urlTracePod struct {
	Name string `json:"name"`
	Phase string `json:"phase"`
	Ready bool `json:"ready"`
	Restarts int32 `json:"restarts"`
}

type urlTraceBackend struct {
	Service       string   `json:"service"`
	Port          string   `json:"port,omitempty"`
	ServiceStatus string   `json:"serviceStatus"`
	EndpointCount int      `json:"endpointCount"`
	ReadyEndpoints int     `json:"readyEndpoints"`
	Pods          []urlTracePod `json:"pods,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

type urlTraceRoute struct {
	Kind      string           `json:"kind"`
	Namespace string           `json:"namespace"`
	Name      string           `json:"name"`
	Host      string           `json:"host"`
	Path      string           `json:"path"`
	Match     string           `json:"match"`
	Backends  []urlTraceBackend `json:"backends,omitempty"`
	Notes     []string         `json:"notes,omitempty"`
}

type urlTraceReport struct {
	URL         string          `json:"url"`
	Namespace   string          `json:"namespace"`
	Host        string          `json:"host"`
	Path        string          `json:"path"`
	Verdict     string          `json:"verdict"`
	Routes      []urlTraceRoute `json:"routes,omitempty"`
	Limitations []string        `json:"limitations,omitempty"`
}

func newTraceURLCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	cmd := &cobra.Command{
		Use:   "trace-url URL",
		Short: "Trace an HTTP(S) URL through Kubernetes routes to Services and Pods",
		Long:  "Read-only discovery of matching Ingress and Gateway API HTTPRoute rules, Service backends, EndpointSlices and target Pods. This is configuration-path tracing; it does not send an HTTP request or prove external reachability.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := url.ParseRequestURI(args[0])
			if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
				return fmt.Errorf("URL must be a valid http:// or https:// URL")
			}
			config, err := kube.NewRESTConfig(kubeconfig, contextName, 0)
			if err != nil { return err }
			client, err := kubernetes.NewForConfig(config)
			if err != nil { return fmt.Errorf("create Kubernetes client: %w", err) }
			dyn, dynErr := dynamic.NewForConfig(config)
			if dynErr != nil { return fmt.Errorf("create Kubernetes dynamic client: %w", dynErr) }

			report := urlTraceReport{URL: target.String(), Namespace: namespace, Host: target.Hostname(), Path: target.Path, Verdict: "UNKNOWN"}
			if report.Path == "" { report.Path = "/" }
			routes, notes := discoverIngressRoutes(cmd.Context(), client, namespace, target)
			report.Routes = append(report.Routes, routes...)
			report.Limitations = append(report.Limitations, notes...)
			gatewayRoutes, gatewayNotes := discoverHTTPRoutes(cmd.Context(), dyn, namespace, target)
			report.Routes = append(report.Routes, gatewayRoutes...)
			report.Limitations = append(report.Limitations, gatewayNotes...)
			for i := range report.Routes {
				for j := range report.Routes[i].Backends {
					resolveURLBackend(cmd.Context(), client, namespace, &report.Routes[i].Backends[j])
				}
			}
			sort.Slice(report.Routes, func(i,j int) bool {
				if report.Routes[i].Kind != report.Routes[j].Kind { return report.Routes[i].Kind < report.Routes[j].Kind }
				if report.Routes[i].Name != report.Routes[j].Name { return report.Routes[i].Name < report.Routes[j].Name }
				return report.Routes[i].Path < report.Routes[j].Path
			})
			switch {
			case len(report.Routes) == 0 && len(report.Limitations) == 0:
				report.Verdict = "UNKNOWN"
				report.Limitations = append(report.Limitations, "No matching Ingress or Gateway API HTTPRoute was found in the selected namespace.")
			case len(report.Routes) == 0:
				report.Verdict = "UNKNOWN"
			default:
				report.Verdict = "DISCOVERED"
				for _, route := range report.Routes {
					for _, backend := range route.Backends {
						if backend.ServiceStatus == "MISSING" || backend.ServiceStatus == "MISSING_PORT" || backend.ServiceStatus == "NO_READY_ENDPOINTS" { report.Verdict = "DEGRADED" }
						if backend.ServiceStatus == "UNKNOWN" && report.Verdict == "DISCOVERED" { report.Verdict = "UNKNOWN" }
					}
				}
			}
			if strings.EqualFold(output, "json") {
				enc := json.NewEncoder(os.Stdout); enc.SetIndent("", "  "); return enc.Encode(report)
			}
			if !strings.EqualFold(output, "terminal") && !strings.EqualFold(output, "text") {
				return fmt.Errorf("unsupported output %q (choose terminal or json)", output)
			}
			printURLTrace(report)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace containing routes and Services")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal or json")
	return cmd
}

func discoverIngressRoutes(ctx context.Context, client kubernetes.Interface, namespace string, target *url.URL) ([]urlTraceRoute, []string) {
	list, err := client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, []string{"Ingress discovery unavailable: " + err.Error()}
	}
	var routes []urlTraceRoute
	for i := range list.Items {
		ing := &list.Items[i]
		add := func(host, path, match, service, port string) {
			if !urlHostMatches(host, target.Hostname()) || !urlPathMatches(path, target.Path, match) { return }
			routes = append(routes, urlTraceRoute{Kind:"Ingress", Namespace:namespace, Name:ing.Name, Host:hostOrWildcard(host), Path:path, Match:match,
				Backends:[]urlTraceBackend{{Service:service, Port:port, ServiceStatus:"PENDING"}}})
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil { continue }
			host := rule.Host; if host == "" { host = "*" }
			for _, p := range rule.HTTP.Paths {
				if p.Backend.Service == nil { continue }
				match := "ImplementationSpecific"; if p.PathType != nil { match = string(*p.PathType) }
				path := p.Path; if path == "" { path = "/" }
				b := p.Backend.Service
				add(host, path, match, b.Name, ingressPortString(b.Port.Name, b.Port.Number))
			}
		}
		if len(routes) == 0 && ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			b := ing.Spec.DefaultBackend.Service
			add("*", "/", "Prefix", b.Name, ingressPortString(b.Port.Name, b.Port.Number))
		}
	}
	return routes, nil
}

func discoverHTTPRoutes(ctx context.Context, dyn dynamic.Interface, namespace string, target *url.URL) ([]urlTraceRoute, []string) {
	var notes []string
	for _, version := range []string{"v1", "v1beta1"} {
		gvr := schema.GroupVersionResource{Group:"gateway.networking.k8s.io", Version:version, Resource:"httproutes"}
		list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) { continue }
		if err != nil {
			notes = append(notes, "Gateway API HTTPRoute discovery ("+version+") unavailable: "+err.Error())
			return nil, notes
		}
		var routes []urlTraceRoute
		for i := range list.Items {
			route := &list.Items[i]
			hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
			matchedHost := ""
			if len(hostnames) == 0 { matchedHost = "*" } else {
				for _, h := range hostnames { if urlHostMatches(h, target.Hostname()) { matchedHost = h; break } }
			}
			if matchedHost == "" { continue }
			rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
			for _, rawRule := range rules {
				rule, ok := rawRule.(map[string]interface{}); if !ok { continue }
				matchName := "default"
				pathMatch := "/"
				matches, _, _ := unstructured.NestedSlice(rule, "matches")
				matched := len(matches) == 0
				for _, rawMatch := range matches {
					m, ok := rawMatch.(map[string]interface{}); if !ok { continue }
					path, _, _ := unstructured.NestedMap(m, "path")
					value, _, _ := unstructured.NestedString(path, "value")
					kind, _, _ := unstructured.NestedString(path, "type")
					if value == "" { value = "/" }; if kind == "" { kind = "PathPrefix" }
					if urlPathMatches(value, target.Path, kind) { matched = true; pathMatch = value; matchName = kind; break }
				}
				if !matched { continue }
				backends, _, _ := unstructured.NestedSlice(rule, "backendRefs")
				var resolved []urlTraceBackend
				for _, rawBackend := range backends {
					b, ok := rawBackend.(map[string]interface{}); if !ok { continue }
					kind, _, _ := unstructured.NestedString(b, "kind")
					if kind != "" && kind != "Service" { continue }
					service, _, _ := unstructured.NestedString(b, "name")
					port, _, _ := unstructured.NestedInt64(b, "port")
					backendNamespace, _, _ := unstructured.NestedString(b, "namespace")
					if backendNamespace != "" && backendNamespace != namespace {
						notes = append(notes, fmt.Sprintf("HTTPRoute %s references cross-namespace Service %s/%s; cross-namespace backend resolution is not supported yet.", route.GetName(), backendNamespace, service))
						continue
					}
					if service == "" { continue }
					resolved = append(resolved, urlTraceBackend{Service:service, Port:fmt.Sprint(port), ServiceStatus:"PENDING"})
				}
				routes = append(routes, urlTraceRoute{Kind:"HTTPRoute", Namespace:namespace, Name:route.GetName(), Host:matchedHost, Path:pathMatch, Match:matchName, Backends:resolved, Notes:[]string{"Parent Gateway listener attachment and readiness are not verified yet."}})
			}
		}
		return routes, notes
	}
	notes = append(notes, "Gateway API HTTPRoute CRD was not found (checked gateway.networking.k8s.io/v1 and v1beta1).")
	return nil, notes
}

func resolveURLBackend(ctx context.Context, client kubernetes.Interface, namespace string, backend *urlTraceBackend) {
	svc, err := client.CoreV1().Services(namespace).Get(ctx, backend.Service, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) { backend.ServiceStatus = "MISSING"; backend.Notes = append(backend.Notes, "Backend Service does not exist.") } else { backend.ServiceStatus = "UNKNOWN"; backend.Notes = append(backend.Notes, "Could not read Service: "+err.Error()) }
		return
	}
	if backend.Port != "" && backend.Port != "0" {
		found := false
		for _, p := range svc.Spec.Ports {
			if fmt.Sprint(p.Port) == backend.Port || p.Name == backend.Port { found = true; break }
		}
		if !found { backend.ServiceStatus = "MISSING_PORT"; backend.Notes = append(backend.Notes, "Route backend port does not match a declared Service port."); return }
	}
	slices, err := client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector:"kubernetes.io/service-name="+backend.Service})
	if err != nil {
		backend.ServiceStatus = "UNKNOWN"; backend.Notes = append(backend.Notes, "Could not list EndpointSlices: "+err.Error()); return
	}
	seenPods := map[string]struct{}{}
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			backend.EndpointCount++
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready { continue }
			backend.ReadyEndpoints++
			if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" {
				name := endpoint.TargetRef.Name
				if endpoint.TargetRef.Namespace != "" && endpoint.TargetRef.Namespace != namespace { continue }
				if _, ok := seenPods[name]; !ok {
					pod, getErr := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
					if getErr == nil { backend.Pods = append(backend.Pods, summarizeURLPod(pod)); seenPods[name] = struct{}{} }
					if getErr != nil { backend.Notes = append(backend.Notes, "Could not read target Pod "+name+": "+getErr.Error()) }
				}
			} else {
				for _, address := range endpoint.Addresses {
					pods, listErr := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{FieldSelector:"status.podIP="+address})
					if listErr == nil { for _, p := range pods.Items { if _, ok := seenPods[p.Name]; !ok { backend.Pods = append(backend.Pods, summarizeURLPod(&p)); seenPods[p.Name]=struct{}{} } } }
				}
			}
		}
	}
	sort.Slice(backend.Pods, func(i, j int) bool { return backend.Pods[i].Name < backend.Pods[j].Name })
	if backend.ReadyEndpoints == 0 { backend.ServiceStatus = "NO_READY_ENDPOINTS"; backend.Notes = append(backend.Notes, "Service has no ready EndpointSlice endpoints.") } else { backend.ServiceStatus = "READY" }
}

func summarizeURLPod(pod *corev1.Pod) urlTracePod {
	var restarts int32
	for _, status := range pod.Status.ContainerStatuses { restarts += status.RestartCount }
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady { ready = condition.Status == corev1.ConditionTrue; break }
	}
	return urlTracePod{Name:pod.Name, Phase:string(pod.Status.Phase), Ready:ready, Restarts:restarts}
}

func urlHostMatches(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if pattern == "" || pattern == "*" { return true }
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		if !strings.HasSuffix(host, suffix) { return false }
		label := strings.TrimSuffix(host, suffix)
		return label != "" && !strings.Contains(label, ".")
	}
	return pattern == host
}

func urlPathMatches(rulePath, requestPath, kind string) bool {
	if requestPath == "" { requestPath = "/" }
	switch kind {
	case "Exact": return requestPath == rulePath
	case "Prefix", "PathPrefix":
		if rulePath == "/" { return true }
		return requestPath == rulePath || strings.HasPrefix(requestPath, strings.TrimSuffix(rulePath, "/")+"/")
	case "ImplementationSpecific":
		return requestPath == rulePath || strings.HasPrefix(requestPath, strings.TrimSuffix(rulePath, "/")+"/")
	default: return false
	}
}

func ingressPortString(name string, number int32) string {
	if name != "" { return name }; if number != 0 { return fmt.Sprint(number) }; return ""
}
func hostOrWildcard(host string) string { if host == "" { return "*" }; return host }

func printURLTrace(report urlTraceReport) {
	fmt.Printf("KTRX URL TRACE  %s\n", report.URL)
	fmt.Printf("Verdict: %s  Namespace: %s\n", report.Verdict, report.Namespace)
	if len(report.Routes) == 0 { fmt.Println("No matching route was established.") }
	for _, route := range report.Routes {
		fmt.Printf("\n%s %s/%s  host=%s path=%s (%s)\n", route.Kind, route.Namespace, route.Name, route.Host, route.Path, route.Match)
		if len(route.Backends) == 0 { fmt.Println("  • No supported Service backend was discovered.") }
		for _, routeNote := range route.Notes { fmt.Printf("  ! %s\n", routeNote) }
		for _, b := range route.Backends {
			fmt.Printf("  → Service %s port=%s status=%s endpoints=%d ready=%d\n", b.Service, value(b.Port), b.ServiceStatus, b.EndpointCount, b.ReadyEndpoints)
			for _, pod := range b.Pods { fmt.Printf("      → Pod %s phase=%s ready=%t restarts=%d\n", pod.Name, pod.Phase, pod.Ready, pod.Restarts) }
			for _, note := range b.Notes { fmt.Printf("      ! %s\n", note) }
		}
	}
	for _, note := range report.Limitations { fmt.Printf("\nNote: %s\n", note) }
	fmt.Println("\nNote: This traces Kubernetes routing configuration only; it does not test DNS, TLS, external load balancers or live HTTP reachability.")
}

