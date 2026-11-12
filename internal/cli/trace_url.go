package cli

import (
	"io"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/Harry10799/ktrx/internal/analyzer"
	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/spf13/cobra"
)

type urlTracePod struct {
	Name                string   `json:"name"`
	Phase               string   `json:"phase"`
	Ready               bool     `json:"ready"`
	Restarts            int32    `json:"restarts"`
	EndpointReady       bool     `json:"endpointReady"`
	ReadinessProbePaths []string `json:"readinessProbePaths,omitempty"`
	Findings            []string `json:"findings,omitempty"`
	NextCommand         string   `json:"nextCommand,omitempty"`
}

type urlTraceBackend struct {
	Namespace           string                       `json:"namespace"`
	Service             string                       `json:"service"`
	Port                string                       `json:"port,omitempty"`
	ServicePort         int32                        `json:"servicePort,omitempty"`
	ServiceStatus       string                       `json:"serviceStatus"`
	EndpointCount       int                          `json:"endpointCount"`
	ReadyEndpoints      int                          `json:"readyEndpoints"`
	NotReadyEndpoints   int                          `json:"notReadyEndpoints"`
	TargetGroupBindings []urlTraceTargetGroupBinding `json:"targetGroupBindings,omitempty"`
	Pods                []urlTracePod                `json:"pods,omitempty"`
	Notes               []string                     `json:"notes,omitempty"`
	ReadinessProbePaths []string                     `json:"readinessProbePaths,omitempty"`
	HealthcheckMismatch bool                         `json:"healthcheckMismatch,omitempty"`
	ALBHealthcheckPath  string                       `json:"albHealthcheckPath,omitempty"`
}

type urlTraceTargetGroupBinding struct {
	Name           string `json:"name"`
	TargetGroupARN string `json:"targetGroupARN,omitempty"`
	TargetType     string `json:"targetType,omitempty"`
}

type urlTraceRoute struct {
	Kind               string            `json:"kind"`
	Namespace          string            `json:"namespace"`
	Name               string            `json:"name"`
	Host               string            `json:"host"`
	Path               string            `json:"path"`
	Match              string            `json:"match"`
	IngressClass       string            `json:"ingressClass,omitempty"`
	Selected           bool              `json:"selected,omitempty"`
	ALBHealthcheckPath string            `json:"albHealthcheckPath,omitempty"`
	RouteStatus        string            `json:"routeStatus,omitempty"`
	LoadBalancerHosts  []string          `json:"loadBalancerHosts,omitempty"`
	Backends           []urlTraceBackend `json:"backends,omitempty"`
	Notes              []string          `json:"notes,omitempty"`
}

type urlTraceReport struct {
	URL         string          `json:"url"`
	Namespace   string          `json:"namespace"`
	Host        string          `json:"host"`
	Path        string          `json:"path"`
	Verdict     string          `json:"verdict"`
	Routes      []urlTraceRoute `json:"routes,omitempty"`
	Limitations []string        `json:"limitations,omitempty"`
	DNS         *urlTraceDNS    `json:"dns,omitempty"`
	TLS         *urlTraceTLS    `json:"tls,omitempty"`
}

type urlTraceDNS struct {
	Resolved    []string `json:"resolved,omitempty"`
	CNAME       string   `json:"cname,omitempty"`
	ALBResolved []string `json:"albResolved,omitempty"`
	MatchesALB  bool     `json:"matchesALB"`
	Checked     bool     `json:"checked"`
	Status      string   `json:"status,omitempty"`
	MatchMethod string   `json:"matchMethod,omitempty"`
	PointsTo    string   `json:"pointsTo,omitempty"`
	Error       string   `json:"error,omitempty"`
}
type urlTraceTLS struct {
	ServerName    string `json:"serverName"`
	NotAfter      string `json:"notAfter,omitempty"`
	DaysRemaining int    `json:"daysRemaining,omitempty"`
	Status        string `json:"status,omitempty"`
	Error         string `json:"error,omitempty"`
}

func newTraceURLCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output, ingressClass string
	var allNamespaces, checkDNS, checkTLS bool
	cmd := &cobra.Command{
		Use:   "trace-url URL",
		Short: "Trace an HTTP(S) URL through Kubernetes routes to Services and Pods",
		Long:  "Read-only discovery of matching Ingress and Gateway API HTTPRoute rules, Service backends, EndpointSlices and target Pods. Optional --check-dns and --check-tls flags perform explicit live DNS/TLS checks; no HTTP request is sent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := url.ParseRequestURI(args[0])
			if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
				return fmt.Errorf("URL must be a valid http:// or https:// URL")
			}
			config, err := kube.NewRESTConfig(kubeconfig, contextName, 0)
			if err != nil {
				return err
			}
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				return fmt.Errorf("create Kubernetes client: %w", err)
			}
			dyn, dynErr := dynamic.NewForConfig(config)
			if dynErr != nil {
				return fmt.Errorf("create Kubernetes dynamic client: %w", dynErr)
			}

			report := urlTraceReport{URL: target.String(), Namespace: namespace, Host: target.Hostname(), Path: target.Path, Verdict: "UNKNOWN"}
			if checkDNS {
				report.DNS = lookupURLDNS(cmd.Context(), target.Hostname())
			}
			if checkTLS && target.Scheme == "https" {
				report.TLS = checkURLTLS(cmd.Context(), target.Hostname(), target.Port())
			}
			if allNamespaces {
				report.Namespace = "*"
			}
			if report.Path == "" {
				report.Path = "/"
			}
			routes, notes := discoverIngressRoutesFiltered(cmd.Context(), client, namespace, allNamespaces, ingressClass, target)
			report.Routes = append(report.Routes, routes...)
			report.Limitations = append(report.Limitations, notes...)
			gatewayRoutes, gatewayNotes := discoverHTTPRoutesInScope(cmd.Context(), dyn, namespace, allNamespaces, target)
			report.Routes = append(report.Routes, gatewayRoutes...)
			if report.DNS != nil {
				report.DNS.Checked = true
				report.DNS.MatchesALB, report.DNS.MatchMethod, report.DNS.PointsTo = dnsMatchesALB(cmd.Context(), report.DNS, report.Routes)
			}
			report.Limitations = append(report.Limitations, gatewayNotes...)
			diagnosisBudget := 0
			for i := range report.Routes {
				for j := range report.Routes[i].Backends {
					resolveURLBackendWithBudget(cmd.Context(), client, report.Routes[i].Namespace, &report.Routes[i].Backends[j], &diagnosisBudget)
					resolveTargetGroupBindings(cmd.Context(), dyn, report.Routes[i].Backends[j].Namespace, &report.Routes[i].Backends[j], &report.Limitations)
				}
			}
			sort.SliceStable(report.Routes, func(i, j int) bool {
				left, right := urlRoutePrecedence(report.Routes[i]), urlRoutePrecedence(report.Routes[j])
				if left != right {
					return left > right
				}
				if report.Routes[i].Namespace != report.Routes[j].Namespace {
					return report.Routes[i].Namespace < report.Routes[j].Namespace
				}
				return report.Routes[i].Name < report.Routes[j].Name
			})
			for i := range report.Routes {
				report.Routes[i].Selected = i == 0
				for j := range report.Routes[i].Backends {
					b := &report.Routes[i].Backends[j]
					healthPath := report.Routes[i].ALBHealthcheckPath
					if b.ALBHealthcheckPath != "" {
						healthPath = b.ALBHealthcheckPath
					}
					if healthPath != "" && len(b.ReadinessProbePaths) > 0 {
						for _, path := range b.ReadinessProbePaths {
							if path != healthPath {
								b.HealthcheckMismatch = true
								b.Notes = append(b.Notes, fmt.Sprintf("Informational: ALB healthcheck path %q differs from Pod readiness probe path %q; this can be intentional.", healthPath, path))
								break
							}
						}
					}
				}
			}
			switch {
			case len(report.Routes) == 0 && len(report.Limitations) == 0:
				report.Verdict = "UNKNOWN"
				report.Limitations = append(report.Limitations, "No matching Ingress or Gateway API HTTPRoute was found in the selected namespace.")
			case len(report.Routes) == 0:
				report.Verdict = "UNKNOWN"
			default:
				report.Verdict = "HEALTHY"
				selected := report.Routes[0]
				if len(selected.Backends) == 0 {
					report.Verdict = "UNKNOWN"
				}
				if selected.RouteStatus == "FAILED" {
					report.Verdict = "FAILED"
				} else if selected.RouteStatus == "UNKNOWN" {
					report.Verdict = "UNKNOWN"
				}
				for _, backend := range selected.Backends {
					switch backend.ServiceStatus {
					case "MISSING", "MISSING_PORT", "NO_READY_ENDPOINTS":
						report.Verdict = "FAILED"
					case "PARTIALLY_READY":
						if report.Verdict != "FAILED" {
							report.Verdict = "WARN"
						}
					case "UNKNOWN", "PENDING":
						if report.Verdict == "HEALTHY" {
							report.Verdict = "UNKNOWN"
						}
					}
				}
			}
			applyURLExternalCheckVerdict(&report)
			if strings.EqualFold(output, "json") {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			if strings.EqualFold(output, "markdown") {
				return printURLTraceMarkdown(cmd.OutOrStdout(), report)
			}
			if !strings.EqualFold(output, "terminal") && !strings.EqualFold(output, "text") {
				return fmt.Errorf("unsupported output %q (choose terminal, json or markdown)", output)
			}
			printURLTrace(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace containing routes and Services")
	cmd.Flags().BoolVar(&allNamespaces, "all-namespaces", false, "Search routes and Services across all namespaces")
	cmd.Flags().StringVar(&ingressClass, "ingress-class", "", "Only inspect Ingresses using this IngressClass")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	cmd.Flags().BoolVar(&checkDNS, "check-dns", false, "Perform an opt-in DNS lookup for the URL hostname")
	cmd.Flags().BoolVar(&checkTLS, "check-tls", false, "Perform an opt-in TLS/SNI handshake and certificate expiry check")
	return cmd
}

func applyURLExternalCheckVerdict(report *urlTraceReport) {
	if report.DNS != nil {
		switch report.DNS.Status {
		case "FAILED":
			report.Verdict = "FAILED"
		case "HEALTHY":
			if report.DNS.MatchMethod != "" && !report.DNS.MatchesALB && report.Verdict != "FAILED" {
				report.Verdict = "WARN"
			}
		}
	}
	if report.TLS != nil {
		switch report.TLS.Status {
		case "FAILED":
			report.Verdict = "FAILED"
		case "HEALTHY":
			if report.TLS.DaysRemaining <= 14 && report.Verdict != "FAILED" {
				report.Verdict = "WARN"
			}
		}
	}
}

func discoverIngressRoutes(ctx context.Context, client kubernetes.Interface, namespace string, target *url.URL) ([]urlTraceRoute, []string) {
	return discoverIngressRoutesFiltered(ctx, client, namespace, false, "", target)
}

func discoverIngressRoutesFiltered(ctx context.Context, client kubernetes.Interface, namespace string, allNamespaces bool, ingressClass string, target *url.URL) ([]urlTraceRoute, []string) {
	listNamespace := namespace
	if allNamespaces {
		listNamespace = ""
	}
	list, err := client.NetworkingV1().Ingresses(listNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, []string{"Ingress discovery unavailable: " + err.Error()}
	}
	var routes []urlTraceRoute
	for i := range list.Items {
		ing := &list.Items[i]
		if ingressClass != "" {
			if ingressClassName(ing) != ingressClass {
				continue
			}
		}
		routeNamespace := ing.Namespace
		if routeNamespace == "" {
			routeNamespace = namespace
		}
		routeStart := len(routes)
		add := func(host, path, match, service, port string) {
			if !urlHostMatches(host, target.Hostname()) || !urlPathMatches(path, target.Path, match) {
				return
			}
			routes = append(routes, urlTraceRoute{Kind: "Ingress", Namespace: routeNamespace, Name: ing.Name, Host: hostOrWildcard(host), Path: path, Match: match, IngressClass: ingressClassName(ing), LoadBalancerHosts: ingressLoadBalancerHosts(ing), ALBHealthcheckPath: ing.Annotations["alb.ingress.kubernetes.io/healthcheck-path"],
				Backends: []urlTraceBackend{{Namespace: routeNamespace, Service: service, Port: port, ServiceStatus: "PENDING"}}})
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			host := rule.Host
			if host == "" {
				host = "*"
			}
			for _, p := range rule.HTTP.Paths {
				if p.Backend.Service == nil {
					continue
				}
				match := "ImplementationSpecific"
				if p.PathType != nil {
					match = string(*p.PathType)
				}
				path := p.Path
				if path == "" {
					path = "/"
				}
				b := p.Backend.Service
				add(host, path, match, b.Name, ingressPortString(b.Port.Name, b.Port.Number))
			}
		}
		if len(routes) == routeStart && ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			b := ing.Spec.DefaultBackend.Service
			add("*", "/", "Prefix", b.Name, ingressPortString(b.Port.Name, b.Port.Number))
		}
	}
	return routes, nil
}

func discoverHTTPRoutes(ctx context.Context, dyn dynamic.Interface, namespace string, target *url.URL) ([]urlTraceRoute, []string) {
	return discoverHTTPRoutesInScope(ctx, dyn, namespace, false, target)
}

func discoverHTTPRoutesInScope(ctx context.Context, dyn dynamic.Interface, namespace string, allNamespaces bool, target *url.URL) ([]urlTraceRoute, []string) {
	var notes []string
	for _, version := range []string{"v1", "v1beta1"} {
		gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: version, Resource: "httproutes"}
		var list *unstructured.UnstructuredList
		var err error
		if allNamespaces {
			list, err = dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		} else {
			list, err = dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		}
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			notes = append(notes, "Gateway API HTTPRoute discovery ("+version+") unavailable: "+err.Error())
			return nil, notes
		}
		var routes []urlTraceRoute
		for i := range list.Items {
			route := &list.Items[i]
			hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
			matchedHost := ""
			if len(hostnames) == 0 {
				matchedHost = "*"
			} else {
				for _, h := range hostnames {
					if urlHostMatches(h, target.Hostname()) {
						matchedHost = h
						break
					}
				}
			}
			if matchedHost == "" {
				continue
			}
			rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
			for _, rawRule := range rules {
				rule, ok := rawRule.(map[string]interface{})
				if !ok {
					continue
				}
				matchName := "default"
				pathMatch := "/"
				matches, _, _ := unstructured.NestedSlice(rule, "matches")
				matched := len(matches) == 0
				for _, rawMatch := range matches {
					m, ok := rawMatch.(map[string]interface{})
					if !ok {
						continue
					}
					path, _, _ := unstructured.NestedMap(m, "path")
					value, _, _ := unstructured.NestedString(path, "value")
					kind, _, _ := unstructured.NestedString(path, "type")
					if value == "" {
						value = "/"
					}
					if kind == "" {
						kind = "PathPrefix"
					}
					if urlPathMatches(value, target.Path, kind) {
						matched = true
						pathMatch = value
						matchName = kind
						break
					}
				}
				if !matched {
					continue
				}
				routeStatus, statusNotes := httpRouteStatus(route)
				backends, _, _ := unstructured.NestedSlice(rule, "backendRefs")
				var resolved []urlTraceBackend
				var routeNotes []string
				for _, rawBackend := range backends {
					b, ok := rawBackend.(map[string]interface{})
					if !ok {
						continue
					}
					kind, _, _ := unstructured.NestedString(b, "kind")
					if kind != "" && kind != "Service" {
						continue
					}
					service, _, _ := unstructured.NestedString(b, "name")
					port, _, _ := unstructured.NestedInt64(b, "port")
					backendNamespace, _, _ := unstructured.NestedString(b, "namespace")
					if backendNamespace == "" {
						backendNamespace = route.GetNamespace()
					}
					if backendNamespace != route.GetNamespace() {
						grantStatus, grantNote := httpRouteReferenceGrantStatus(ctx, dyn, route.GetNamespace(), backendNamespace, service)
						switch grantStatus {
						case "ALLOWED":
							// The ReferenceGrant explicitly permits this backend.
						case "DENIED":
							routeStatus = "FAILED"
							routeNotes = append(routeNotes, grantNote+" Backend was not resolved.")
							continue
						default:
							if routeStatus != "FAILED" {
								routeStatus = "UNKNOWN"
							}
							routeNotes = append(routeNotes, grantNote+" Backend was not resolved.")
							continue
						}
					}
					if service == "" {
						continue
					}
					resolved = append(resolved, urlTraceBackend{Namespace: backendNamespace, Service: service, Port: fmt.Sprint(port), ServiceStatus: "PENDING"})
				}
				statusNotes = append(statusNotes, routeNotes...)
				routes = append(routes, urlTraceRoute{Kind: "HTTPRoute", Namespace: route.GetNamespace(), Name: route.GetName(), Host: matchedHost, Path: pathMatch, Match: matchName, RouteStatus: routeStatus, Backends: resolved, Notes: statusNotes})
			}
		}
		return routes, notes
	}
	notes = append(notes, "Gateway API HTTPRoute CRD was not found (checked gateway.networking.k8s.io/v1 and v1beta1).")
	return nil, notes
}

func resolveURLBackend(ctx context.Context, client kubernetes.Interface, namespace string, backend *urlTraceBackend) {
	budget := 0
	resolveURLBackendWithBudget(ctx, client, namespace, backend, &budget)
}

func resolveURLBackendWithBudget(ctx context.Context, client kubernetes.Interface, namespace string, backend *urlTraceBackend, diagnosisBudget *int) {
	if backend.Namespace == "" {
		backend.Namespace = namespace
	}
	namespace = backend.Namespace
	svc, err := client.CoreV1().Services(namespace).Get(ctx, backend.Service, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			backend.ServiceStatus = "MISSING"
			backend.Notes = append(backend.Notes, "Backend Service does not exist.")
		} else {
			backend.ServiceStatus = "UNKNOWN"
			backend.Notes = append(backend.Notes, "Could not read Service: "+err.Error())
		}
		return
	}
	backend.ALBHealthcheckPath = svc.Annotations["alb.ingress.kubernetes.io/healthcheck-path"]
	if backend.Port != "" && backend.Port != "0" {
		found := false
		for _, p := range svc.Spec.Ports {
			if fmt.Sprint(p.Port) == backend.Port || p.Name == backend.Port {
				found = true
				backend.ServicePort = p.Port
				break
			}
		}
		if !found {
			backend.ServiceStatus = "MISSING_PORT"
			backend.Notes = append(backend.Notes, "Route backend port does not match a declared Service port.")
			return
		}
	}
	slices, err := client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + backend.Service})
	if err != nil {
		backend.ServiceStatus = "UNKNOWN"
		backend.Notes = append(backend.Notes, "Could not list EndpointSlices: "+err.Error())
		return
	}
	seenPods := map[string]struct{}{}
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			backend.EndpointCount++
			endpointReady := endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready
			if endpointReady {
				backend.ReadyEndpoints++
			}
			if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" {
				name := endpoint.TargetRef.Name
				if endpoint.TargetRef.Namespace != "" && endpoint.TargetRef.Namespace != namespace {
					continue
				}
				if _, ok := seenPods[name]; !ok {
					pod, getErr := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
					if getErr == nil {
						backend.Pods = append(backend.Pods, summarizeURLPod(pod, endpointReady))
						seenPods[name] = struct{}{}
					}
					if getErr != nil {
						backend.Notes = append(backend.Notes, "Could not read target Pod "+name+": "+getErr.Error())
					}
				}
			} else {
				for _, address := range endpoint.Addresses {
					pods, listErr := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{FieldSelector: "status.podIP=" + address})
					if listErr == nil {
						for _, p := range pods.Items {
							if _, ok := seenPods[p.Name]; !ok {
								backend.Pods = append(backend.Pods, summarizeURLPod(&p, endpointReady))
								seenPods[p.Name] = struct{}{}
							}
						}
					}
				}
			}
		}
	}
	sort.Slice(backend.Pods, func(i, j int) bool { return backend.Pods[i].Name < backend.Pods[j].Name })
	for i := range backend.Pods {
		if !backend.Pods[i].Ready || !backend.Pods[i].EndpointReady {
			diagnoseURLPodWithBudget(ctx, client, namespace, &backend.Pods[i], diagnosisBudget)
		}
	}
	for _, pod := range backend.Pods {
		backend.ReadinessProbePaths = append(backend.ReadinessProbePaths, pod.ReadinessProbePaths...)
	}
	backend.ReadinessProbePaths = uniqueSorted(backend.ReadinessProbePaths)
	backend.NotReadyEndpoints = backend.EndpointCount - backend.ReadyEndpoints
	if backend.ReadyEndpoints == 0 {
		backend.ServiceStatus = "NO_READY_ENDPOINTS"
		backend.Notes = append(backend.Notes, "Service has no ready EndpointSlice endpoints.")
	} else if backend.NotReadyEndpoints > 0 {
		backend.ServiceStatus = "PARTIALLY_READY"
		backend.Notes = append(backend.Notes, "One or more EndpointSlice targets are not ready.")
	} else {
		backend.ServiceStatus = "READY"
	}
}

func summarizeURLPod(pod *corev1.Pod, endpointReady bool) urlTracePod {
	var restarts int32
	for _, status := range pod.Status.ContainerStatuses {
		restarts += status.RestartCount
	}
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			ready = condition.Status == corev1.ConditionTrue
			break
		}
	}
	var probePaths []string
	for _, container := range pod.Spec.Containers {
		if p := container.ReadinessProbe; p != nil && p.HTTPGet != nil {
			probePaths = append(probePaths, p.HTTPGet.Path)
		}
	}
	return urlTracePod{Name: pod.Name, Phase: string(pod.Status.Phase), Ready: ready, Restarts: restarts, EndpointReady: endpointReady, ReadinessProbePaths: uniqueSorted(probePaths)}
}

func appendURLTraceNote(notes []string, note string) []string {
	for _, existing := range notes {
		if existing == note {
			return notes
		}
	}
	return append(notes, note)
}

func ingressClassName(ing *networkingv1.Ingress) string {
	if ing.Spec.IngressClassName != nil {
		return *ing.Spec.IngressClassName
	}
	if legacy := ing.Annotations["kubernetes.io/ingress.class"]; legacy != "" {
		return legacy
	}
	return ""
}

func ingressLoadBalancerHosts(ing *networkingv1.Ingress) []string {
	hosts := make([]string, 0, len(ing.Status.LoadBalancer.Ingress))
	for _, item := range ing.Status.LoadBalancer.Ingress {
		if item.Hostname != "" {
			hosts = append(hosts, item.Hostname)
		} else if item.IP != "" {
			hosts = append(hosts, item.IP)
		}
	}
	sort.Strings(hosts)
	return hosts
}

func resolveTargetGroupBindings(ctx context.Context, dyn dynamic.Interface, namespace string, backend *urlTraceBackend, limitations *[]string) {
	if dyn == nil {
		return
	}
	gvr := schema.GroupVersionResource{Group: "elbv2.k8s.aws", Version: "v1beta1", Resource: "targetgroupbindings"}
	list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		*limitations = appendURLTraceNote(*limitations, "AWS TargetGroupBinding discovery is unavailable (RBAC denied or API error).")
		return
	}
	for _, item := range list.Items {
		service, _, _ := unstructured.NestedString(item.Object, "spec", "serviceRef", "name")
		if service != backend.Service {
			continue
		}
		refPort, hasPort, _ := unstructured.NestedInt64(item.Object, "spec", "serviceRef", "port")
		if hasPort && backend.ServicePort != 0 && int64(backend.ServicePort) != refPort {
			continue
		}
		arn, _, _ := unstructured.NestedString(item.Object, "spec", "targetGroupARN")
		targetType, _, _ := unstructured.NestedString(item.Object, "spec", "targetType")
		backend.TargetGroupBindings = append(backend.TargetGroupBindings, urlTraceTargetGroupBinding{Name: item.GetName(), TargetGroupARN: arn, TargetType: targetType})
	}
	sort.Slice(backend.TargetGroupBindings, func(i, j int) bool { return backend.TargetGroupBindings[i].Name < backend.TargetGroupBindings[j].Name })
}

func urlRoutePrecedence(route urlTraceRoute) int {
	hostRank := 2
	if route.Host == "*" {
		hostRank = 0
	} else if strings.HasPrefix(route.Host, "*.") {
		hostRank = 1
	}
	pathRank := 1
	switch route.Match {
	case "Exact":
		pathRank = 3
	case "Prefix", "PathPrefix":
		pathRank = 2
	case "ImplementationSpecific":
		pathRank = 1
	}
	return hostRank*1000000 + len(route.Path)*10 + pathRank
}

func urlGlobMatch(pattern, value string) (bool, error) {
	var expression strings.Builder
	expression.WriteString("^")
	for _, char := range pattern {
		switch char {
		case '*':
			expression.WriteString(".*")
		case '?':
			expression.WriteString(".")
		default:
			expression.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	expression.WriteString("$")
	compiled, err := regexp.Compile(expression.String())
	if err != nil {
		return false, err
	}
	return compiled.MatchString(value), nil
}

func urlHostMatches(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if pattern == "" || pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		if !strings.HasSuffix(host, suffix) {
			return false
		}
		label := strings.TrimSuffix(host, suffix)
		return label != "" && !strings.Contains(label, ".")
	}
	return pattern == host
}

func urlPathMatches(rulePath, requestPath, kind string) bool {
	if requestPath == "" {
		requestPath = "/"
	}
	switch kind {
	case "Exact":
		return requestPath == rulePath
	case "Prefix", "PathPrefix":
		if rulePath == "/" {
			return true
		}
		return requestPath == rulePath || strings.HasPrefix(requestPath, strings.TrimSuffix(rulePath, "/")+"/")
	case "ImplementationSpecific":
		matched, err := urlGlobMatch(rulePath, requestPath)
		if err == nil && matched {
			return true
		}
		return requestPath == rulePath || strings.HasPrefix(requestPath, strings.TrimSuffix(rulePath, "/")+"/")
	default:
		return false
	}
}

func ingressPortString(name string, number int32) string {
	if name != "" {
		return name
	}
	if number != 0 {
		return fmt.Sprint(number)
	}
	return ""
}
func hostOrWildcard(host string) string {
	if host == "" {
		return "*"
	}
	return host
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func lookupURLDNS(ctx context.Context, host string) *urlTraceDNS {
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(lookupCtx, host)
	result := &urlTraceDNS{Resolved: uniqueSorted(ips)}
	cname, cnameErr := net.DefaultResolver.LookupCNAME(lookupCtx, host)
	if cnameErr == nil {
		result.CNAME = strings.TrimSuffix(strings.ToLower(cname), ".")
	}
	if err != nil {
		result.Error = err.Error()
		if isURLDNSNotFound(err) {
			result.Status = "FAILED"
		} else {
			result.Status = "UNKNOWN"
		}
	} else if len(result.Resolved) == 0 {
		result.Status = "UNKNOWN"
		result.Error = "DNS returned no addresses; resolution status is inconclusive"
	} else {
		result.Status = "HEALTHY"
	}
	return result
}

func isURLDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func dnsMatchesALB(ctx context.Context, dns *urlTraceDNS, routes []urlTraceRoute) (bool, string, string) {
	if dns == nil {
		return false, "", "DNS check unavailable"
	}
	var loadBalancerHosts []string
	for _, route := range routes {
		loadBalancerHosts = append(loadBalancerHosts, route.LoadBalancerHosts...)
	}
	loadBalancerHosts = uniqueSorted(loadBalancerHosts)
	if len(loadBalancerHosts) == 0 {
		return false, "", "No load balancer hostname is available for comparison"
	}
	for _, lbHost := range loadBalancerHosts {
		if dns.CNAME != "" && strings.EqualFold(strings.TrimSuffix(lbHost, "."), dns.CNAME) {
			return true, "CNAME", "CNAME points directly to ALB hostname " + lbHost
		}
	}
	for _, lbHost := range loadBalancerHosts {
		if net.ParseIP(lbHost) != nil {
			dns.ALBResolved = append(dns.ALBResolved, lbHost)
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err := net.DefaultResolver.LookupHost(lookupCtx, lbHost)
		cancel()
		if err == nil {
			dns.ALBResolved = append(dns.ALBResolved, ips...)
		}
	}
	dns.ALBResolved = uniqueSorted(dns.ALBResolved)
	if len(dns.ALBResolved) == 0 {
		return false, "", "Could not resolve the load balancer hostname from this machine; DNS-to-ALB comparison is inconclusive"
	}
	if ipSetsIntersect(dns.Resolved, dns.ALBResolved) {
		return true, "IP", "DNS and ALB hostname have overlapping IP addresses"
	}
	if len(dns.Resolved) == 0 {
		return false, "", "DNS did not resolve"
	}
	if dns.CNAME != "" {
		return false, "CNAME", "DNS points to CNAME " + dns.CNAME + " (resolved IPs: " + strings.Join(dns.Resolved, ", ") + ")"
	}
	return false, "IP", "DNS alias resolves to " + strings.Join(dns.Resolved, ", ") + "; this may be a CloudFront or Global Accelerator front end"
}

func ipSetsIntersect(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		if ip := net.ParseIP(value); ip != nil {
			seen[ip.String()] = struct{}{}
		}
	}
	for _, value := range right {
		if ip := net.ParseIP(value); ip != nil {
			if _, ok := seen[ip.String()]; ok {
				return true
			}
		}
	}
	return false
}

func checkURLTLS(ctx context.Context, host, port string) *urlTraceTLS {
	if port == "" {
		port = "443"
	}
	result := &urlTraceTLS{ServerName: host, Status: "UNKNOWN"}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer conn.Close()
	handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var verificationErr error
	// Verify explicitly so expired certificates can still expose their NotAfter date.
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("TLS peer did not present a certificate")
		}
		intermediates := x509.NewCertPool()
		for _, cert := range state.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		_, verificationErr = state.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: host, Intermediates: intermediates})
		return nil
	}})
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		result.Error = err.Error()
		return result
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		result.Error = "TLS peer did not present a certificate"
		return result
	}
	notAfter := state.PeerCertificates[0].NotAfter
	result.NotAfter = notAfter.UTC().Format(time.RFC3339)
	result.DaysRemaining = int(time.Until(notAfter).Hours() / 24)
	if verificationErr != nil {
		result.Error = verificationErr.Error()
		if isURLCertificateError(verificationErr) {
			result.Status = "FAILED"
		}
		return result
	}
	if result.DaysRemaining < 0 {
		result.Status = "FAILED"
		result.Error = "x509: certificate has expired"
		return result
	}
	result.Status = "HEALTHY"
	return result
}

func isURLCertificateError(err error) bool {
	var invalid x509.CertificateInvalidError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var roots x509.SystemRootsError
	return errors.As(err, &invalid) || errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &roots)
}

func diagnoseURLPod(ctx context.Context, client kubernetes.Interface, namespace string, pod *urlTracePod) {
	budget := 0
	diagnoseURLPodWithBudget(ctx, client, namespace, pod, &budget)
}

func diagnoseURLPodWithBudget(ctx context.Context, client kubernetes.Interface, namespace string, pod *urlTracePod, budget *int) {
	if *budget >= 3 {
		pod.NextCommand = fmt.Sprintf("ktrx %s -n %s", pod.Name, namespace)
		pod.Findings = []string{"Automatic diagnosis limit reached (3 Pods); run the suggested command for full analysis."}
		return
	}
	*budget++
	pod.NextCommand = fmt.Sprintf("ktrx %s -n %s", pod.Name, namespace)
	report, err := (&analyzer.PodAnalyzer{Client: client}).Analyze(ctx, namespace, pod.Name)
	if err != nil {
		pod.Findings = []string{"Pod analyzer could not complete: " + err.Error()}
		return
	}
	for _, evidence := range report.Evidence {
		if evidence.Verdict == analyzer.Healthy || evidence.Verdict == analyzer.Info {
			continue
		}
		pod.Findings = append(pod.Findings, string(evidence.Verdict)+": "+evidence.Check+" — "+evidence.Summary)
		if len(pod.Findings) == 3 {
			break
		}
	}
	if len(pod.Findings) == 0 {
		pod.Findings = append(pod.Findings, "Pod analyzer found no additional non-healthy findings; inspect full Pod report for details.")
	}
}

func httpRouteStatus(route *unstructured.Unstructured) (string, []string) {
	parents, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	if len(parents) == 0 {
		return "UNKNOWN", []string{"HTTPRoute has no status.parents entry; Gateway attachment is not confirmed."}
	}

	healthyParents, failedParents, validParents := 0, 0, 0
	var notes []string
	for _, rawParent := range parents {
		parent, ok := rawParent.(map[string]interface{})
		if !ok {
			continue
		}
		validParents++
		conditions, _, _ := unstructured.NestedSlice(parent, "conditions")
		conditionStatus := map[string]string{}
		conditionReason := map[string]string{}
		for _, rawCondition := range conditions {
			condition, ok := rawCondition.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _, _ := unstructured.NestedString(condition, "type")
			if typ != "Accepted" && typ != "ResolvedRefs" {
				continue
			}
			value, _, _ := unstructured.NestedString(condition, "status")
			reason, _, _ := unstructured.NestedString(condition, "reason")
			conditionStatus[typ] = value
			conditionReason[typ] = reason
		}

		accepted, hasAccepted := conditionStatus["Accepted"]
		resolved, hasResolved := conditionStatus["ResolvedRefs"]
		switch {
		case hasAccepted && accepted == "True" && hasResolved && resolved == "True":
			healthyParents++
		case (hasAccepted && accepted == "False") || (hasResolved && resolved == "False"):
			failedParents++
			if accepted == "False" {
				notes = append(notes, "HTTPRoute Accepted=False: "+conditionReason["Accepted"])
			}
			if resolved == "False" {
				notes = append(notes, "HTTPRoute ResolvedRefs=False: "+conditionReason["ResolvedRefs"])
			}
		default:
			missing := make([]string, 0, 2)
			if !hasAccepted || accepted != "True" {
				missing = append(missing, "Accepted")
			}
			if !hasResolved || resolved != "True" {
				missing = append(missing, "ResolvedRefs")
			}
			notes = append(notes, "HTTPRoute parent status does not confirm "+strings.Join(missing, " and ")+" as True.")
		}
	}

	if validParents == 0 {
		return "UNKNOWN", []string{"HTTPRoute status.parents contains no valid parent status entries."}
	}
	if healthyParents == validParents {
		return "HEALTHY", []string{"All reported HTTPRoute parents have Accepted=True and ResolvedRefs=True."}
	}
	// A route can attach to multiple Gateways. A rejection from one parent must
	// not hide another healthy attachment, and an incomplete parent must not be
	// presented as a definitive failure.
	if failedParents == validParents {
		return "FAILED", notes
	}
	return "UNKNOWN", notes
}

func httpRouteReferenceGrantStatus(ctx context.Context, dyn dynamic.Interface, fromNamespace, toNamespace, serviceName string) (string, string) {
	for _, version := range []string{"v1", "v1beta1"} {
		gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: version, Resource: "referencegrants"}
		list, err := dyn.Resource(gvr).Namespace(toNamespace).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "UNKNOWN", "Could not inspect ReferenceGrants in namespace " + toNamespace + ": " + err.Error()
		}
		for _, grant := range list.Items {
			from, _, _ := unstructured.NestedSlice(grant.Object, "spec", "from")
			to, _, _ := unstructured.NestedSlice(grant.Object, "spec", "to")
			fromAllowed, toAllowed := false, false
			for _, raw := range from {
				item, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				group, _, _ := unstructured.NestedString(item, "group")
				kind, _, _ := unstructured.NestedString(item, "kind")
				ns, _, _ := unstructured.NestedString(item, "namespace")
				if group == "gateway.networking.k8s.io" && kind == "HTTPRoute" && ns == fromNamespace {
					fromAllowed = true
				}
			}
			for _, raw := range to {
				item, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				group, _, _ := unstructured.NestedString(item, "group")
				kind, _, _ := unstructured.NestedString(item, "kind")
				name, _, _ := unstructured.NestedString(item, "name")
				if group == "" && kind == "Service" && (name == "" || name == serviceName) {
					toAllowed = true
				}
			}
			if fromAllowed && toAllowed {
				return "ALLOWED", "A matching ReferenceGrant permits this cross-namespace Service reference."
			}
		}
		return "DENIED", "No matching ReferenceGrant permits HTTPRoute " + fromNamespace + " to reference Service " + toNamespace + "/" + serviceName + "."
	}
	return "UNKNOWN", "ReferenceGrant API is unavailable in both gateway.networking.k8s.io/v1 and v1beta1; permission could not be verified."
}

func printURLTraceMarkdown(w io.Writer, report urlTraceReport) error {
	fmt.Fprintf(w,"# KTRX URL trace: %s\n\n", report.URL)
	fmt.Fprintf(w,"- **Verdict:** %s\n- **Namespace scope:** %s\n- **Host/path:** %s%s\n\n", report.Verdict, report.Namespace, report.Host, report.Path)
	if report.DNS != nil {
		fmt.Fprintf(w,"## DNS\n\n- Status: %s\n- Resolved: %s\n- CNAME: %s\n- Matches ALB: %t (checked=%t, method=%s)\n- DNS target: %s\n- ALB resolved IPs: %s\n- Error: %s\n\n", value(report.DNS.Status), strings.Join(report.DNS.Resolved, ", "), value(report.DNS.CNAME), report.DNS.MatchesALB, report.DNS.Checked, value(report.DNS.MatchMethod), value(report.DNS.PointsTo), strings.Join(report.DNS.ALBResolved, ", "), report.DNS.Error)
	}
	if report.TLS != nil {
		fmt.Fprintf(w,"## TLS/SNI\n\n- Status: %s\n- Server name: %s\n- Certificate expires: %s\n- Days remaining: %d\n- Error: %s\n\n", value(report.TLS.Status), report.TLS.ServerName, report.TLS.NotAfter, report.TLS.DaysRemaining, report.TLS.Error)
	}
	for i, route := range report.Routes {
		label := "also matches"
		if i == 0 {
			label = "SELECTED"
		}
		fmt.Fprintf(w,"## %s %s/%s — %s\n\n", route.Kind, route.Namespace, route.Name, label)
		fmt.Fprintf(w,"- Host/path: %s%s (%s %s)\n- Route status: %s\n", route.Host, route.Path, route.Match, route.IngressClass, route.RouteStatus)
		if route.ALBHealthcheckPath != "" {
			fmt.Fprintf(w,"- ALB healthcheck path: %s\n", route.ALBHealthcheckPath)
		}
		for _, host := range route.LoadBalancerHosts {
			fmt.Fprintf(w,"- Load balancer hostname: %s\n", host)
		}
		for _, note := range route.Notes {
			fmt.Fprintf(w,"- Note: %s\n", note)
		}
		for _, backend := range route.Backends {
			fmt.Fprintf(w,"\n### Service %s/%s — %s\n\n- Port: %s\n- Endpoints: %d total, %d ready, %d not ready\n", backend.Namespace, backend.Service, backend.ServiceStatus, backend.Port, backend.EndpointCount, backend.ReadyEndpoints, backend.NotReadyEndpoints)
			if backend.HealthcheckMismatch {
				fmt.Fprintln(w,"- **ALB healthcheck/readiness path mismatch detected.**")
			}
			for _, binding := range backend.TargetGroupBindings {
				fmt.Fprintf(w,"- TargetGroupBinding: %s (%s)\n", binding.Name, binding.TargetGroupARN)
			}
			for _, pod := range backend.Pods {
				fmt.Fprintf(w,"- Pod %s: phase=%s ready=%t endpointReady=%t restarts=%d\n", pod.Name, pod.Phase, pod.Ready, pod.EndpointReady, pod.Restarts)
				for _, finding := range pod.Findings {
					fmt.Fprintf(w,"  - %s\n", finding)
				}
				if pod.NextCommand != "" {
					fmt.Fprintf(w,"  - Next: %s\n", pod.NextCommand)
				}
			}
			for _, note := range backend.Notes {
				fmt.Fprintf(w,"- Note: %s\n", note)
			}
		}
	}
	if len(report.Limitations) > 0 {
		fmt.Fprintln(w,"\n## Limitations")
		for _, note := range report.Limitations {
			fmt.Fprintf(w,"- %s\n", note)
		}
	}
	fmt.Fprintln(w,"\n> Route discovery is Kubernetes configuration evidence; DNS and TLS are opt-in live checks. This command does not send an HTTP request.")
	return nil
}

func printURLTrace(w io.Writer, report urlTraceReport) {
	fmt.Fprintf(w,"KTRX URL TRACE  %s\n", report.URL)
	fmt.Fprintf(w,"Verdict: %s  Namespace: %s\n", report.Verdict, report.Namespace)
	if len(report.Routes) == 0 {
		fmt.Fprintln(w,"No matching route was established.")
	}
	if report.DNS != nil {
		fmt.Fprintf(w,"\nDNS: status=%s resolved=%s CNAME=%s matchesALB=%t checked=%t method=%s\n", value(report.DNS.Status), strings.Join(report.DNS.Resolved, ","), value(report.DNS.CNAME), report.DNS.MatchesALB, report.DNS.Checked, value(report.DNS.MatchMethod))
		if report.DNS.PointsTo != "" {
			fmt.Fprintf(w,"  DNS target: %s\n", report.DNS.PointsTo)
		}
		if report.DNS.Error != "" {
			fmt.Fprintf(w,"  DNS error: %s\n", report.DNS.Error)
		}
	}
	if report.TLS != nil {
		fmt.Fprintf(w,"TLS: status=%s serverName=%s expires=%s daysRemaining=%d\n", value(report.TLS.Status), report.TLS.ServerName, value(report.TLS.NotAfter), report.TLS.DaysRemaining)
		if report.TLS.Error != "" {
			fmt.Fprintf(w,"  TLS error: %s\n", report.TLS.Error)
		}
	}
	for routeIndex, route := range report.Routes {
		selection := "also matches"
		if routeIndex == 0 {
			selection = "selected"
		}
		fmt.Fprintf(w,"\n%s %s/%s  host=%s path=%s (%s) class=%s\n", route.Kind, route.Namespace, route.Name, route.Host, route.Path, route.Match, value(route.IngressClass))
		fmt.Fprintf(w,"  Route selection: %s\n", selection)
		for _, host := range route.LoadBalancerHosts {
			fmt.Fprintf(w,"  LoadBalancer hostname: %s\n", host)
		}
		if len(route.Backends) == 0 {
			fmt.Fprintln(w,"  • No supported Service backend was discovered.")
		}
		for _, routeNote := range route.Notes {
			fmt.Fprintf(w,"  ! %s\n", routeNote)
		}
		for _, b := range route.Backends {
			fmt.Fprintf(w,"  → Service %s/%s port=%s status=%s endpoints=%d ready=%d notReady=%d\n", b.Namespace, b.Service, value(b.Port), b.ServiceStatus, b.EndpointCount, b.ReadyEndpoints, b.NotReadyEndpoints)
			for _, binding := range b.TargetGroupBindings {
				fmt.Fprintf(w,"      → TargetGroupBinding %s targetGroup=%s targetType=%s\n", binding.Name, binding.TargetGroupARN, binding.TargetType)
			}
			for _, pod := range b.Pods {
				fmt.Fprintf(w,"      → Pod %s phase=%s ready=%t endpointReady=%t restarts=%d\n", pod.Name, pod.Phase, pod.Ready, pod.EndpointReady, pod.Restarts)
				for _, finding := range pod.Findings {
					fmt.Fprintf(w,"          ! %s\n", finding)
				}
				if pod.NextCommand != "" {
					fmt.Fprintf(w,"          Next: %s\n", pod.NextCommand)
				}
			}
			for _, note := range b.Notes {
				fmt.Fprintf(w,"      ! %s\n", note)
			}
		}
	}
	for _, note := range report.Limitations {
		fmt.Fprintf(w,"\nNote: %s\n", note)
	}
	fmt.Fprintln(w,"\nNote: DNS and TLS are live opt-in checks; route discovery alone does not test external HTTP reachability.")
}
