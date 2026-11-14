package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/charmbracelet/lipgloss"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"github.com/spf13/cobra"
)

type topologyNode struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Namespace string           `json:"namespace,omitempty"`
	Name     string            `json:"name"`
	Status   string            `json:"status,omitempty"`
	Address  string            `json:"address,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
}

type topologyEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Evidence string `json:"evidence"`
}

type topologyReport struct {
	Namespace   string          `json:"namespace"`
	GeneratedAt string          `json:"generatedAt"`
	Nodes       []topologyNode  `json:"nodes"`
	Edges       []topologyEdge  `json:"edges"`
	Limitations []string        `json:"limitations,omitempty"`
}

var topologyHostPattern = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]{1,}\b`)

func newTopologyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topology",
		Short: "Explore namespace-scoped Kubernetes topology",
	}
	cmd.AddCommand(newDNSTopologyCommand())
	return cmd
}

func newDNSTopologyCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor bool
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Map Pod-to-DNS and Service relationships in one namespace",
		Long:  "Read-only namespace topology built from Kubernetes API configuration and status. DNS edges are declared references, not a record of historical DNS queries.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(namespace) == "" {
				return fmt.Errorf("namespace must not be empty")
			}
			client, err := kube.NewClient(kubeconfig, contextName)
			if err != nil {
				return err
			}
			pods, err := client.CoreV1().Pods(namespace).List(cmd.Context(), metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("list Pods in namespace %q: %w", namespace, err)
			}
			services, err := client.CoreV1().Services(namespace).List(cmd.Context(), metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("list Services in namespace %q: %w", namespace, err)
			}
			report := buildTopologyReport(namespace, pods.Items, services.Items)
			switch strings.ToLower(output) {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			case "terminal", "text":
				printTopologyTerminal(cmd.OutOrStdout(), report, !noColor && os.Getenv("NO_COLOR") == "")
				return nil
			case "markdown":
				return printTopologyMarkdown(cmd.OutOrStdout(), report)
			default:
				return fmt.Errorf("unsupported output %q (choose terminal, json or markdown)", output)
			}
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace to map")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	return cmd
}

func buildTopologyReport(namespace string, pods []corev1.Pod, services []corev1.Service) topologyReport {
	report := topologyReport{Namespace: namespace, GeneratedAt: metav1.Now().UTC().Format("2006-01-02T15:04:05Z"), Nodes: []topologyNode{}, Edges: []topologyEdge{}}
	serviceByDNS := make(map[string]string, len(services)*4)
	for _, svc := range services {
		id := "service/" + svc.Name
		status := "ClusterIP"
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			status = "LoadBalancer"
		} else if svc.Spec.Type == corev1.ServiceTypeNodePort {
			status = "NodePort"
		} else if svc.Spec.ClusterIP == corev1.ClusterIPNone {
			status = "Headless"
		}
		address := svc.Spec.ClusterIP
		if address == corev1.ClusterIPNone {
			address = "headless"
		}
		report.Nodes = append(report.Nodes, topologyNode{ID: id, Kind: "Service", Namespace: namespace, Name: svc.Name, Status: status, Address: address, Details: map[string]string{"type": string(svc.Spec.Type), "selector": labels.Set(svc.Spec.Selector).String()}})
		serviceByDNS[strings.ToLower(svc.Name)] = id
		serviceByDNS[strings.ToLower(svc.Name+"."+namespace)] = id
		serviceByDNS[strings.ToLower(svc.Name+"."+namespace+".svc")] = id
		serviceByDNS[strings.ToLower(svc.Name+"."+namespace+".svc.cluster.local")] = id
	}
	for _, pod := range pods {
		podID := "pod/" + pod.Name
		ready := podReadyForTopology(pod)
		status := string(pod.Status.Phase)
		if ready {
			status += "/Ready"
		} else {
			status += "/NotReady"
		}
		dnsNameservers, dnsSearches, dnsOptions := "", "", ""
		if pod.Spec.DNSConfig != nil {
			dnsNameservers = strings.Join(pod.Spec.DNSConfig.Nameservers, ", ")
			dnsSearches = strings.Join(pod.Spec.DNSConfig.Searches, ", ")
			for _, option := range pod.Spec.DNSConfig.Options {
				if option != nil {
					optionValue := option.Name
					if option.Value != "" {
						optionValue += "=" + option.Value
					}
					dnsOptions = appendCommaValue(dnsOptions, optionValue)
				}
			}
		}
		details := map[string]string{
			"node": pod.Spec.NodeName,
			"dnsPolicy": string(pod.Spec.DNSPolicy),
			"hostNetwork": fmt.Sprintf("%t", pod.Spec.HostNetwork),
			"dnsNameservers": dnsNameservers,
			"dnsSearches": dnsSearches,
			"dnsOptions": dnsOptions,
		}
		report.Nodes = append(report.Nodes, topologyNode{ID: podID, Kind: "Pod", Namespace: namespace, Name: pod.Name, Status: status, Address: pod.Status.PodIP, Details: details})
		for _, svc := range services {
			if len(svc.Spec.Selector) == 0 {
				continue
			}
			selector := labels.Set(svc.Spec.Selector).AsSelector()
			if selector.Empty() || !selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
			report.Edges = append(report.Edges, topologyEdge{From: "service/" + svc.Name, To: podID, Relation: "selects", Evidence: "Service selector matches Pod labels; endpoint readiness is not inferred from selector alone"})
		}
		for _, ref := range podDNSReferences(pod) {
			hostID := "dns/" + ref.Host
			if !topologyHasNode(report.Nodes, hostID) {
				report.Nodes = append(report.Nodes, topologyNode{ID: hostID, Kind: "DNS", Name: ref.Host, Status: "Declared reference", Details: map[string]string{"source": strings.Join(ref.Sources, ", ")}})
			} else {
				for i := range report.Nodes {
					if report.Nodes[i].ID == hostID {
						old := report.Nodes[i].Details["source"]
						report.Nodes[i].Details["source"] = old + ", " + strings.Join(ref.Sources, ", ")
					}
				}
			}
			report.Edges = append(report.Edges, topologyEdge{From: podID, To: hostID, Relation: "references", Evidence: "Hostname found in Pod container configuration"})
			if serviceID, ok := serviceByDNS[strings.ToLower(ref.Host)]; ok {
				report.Edges = append(report.Edges, topologyEdge{From: hostID, To: serviceID, Relation: "matches Service DNS name", Evidence: "Name matches Kubernetes Service DNS convention; DNS query was not executed"})
			}
		}
	}
	sort.Slice(report.Nodes, func(i, j int) bool { return report.Nodes[i].ID < report.Nodes[j].ID })
	sort.Slice(report.Edges, func(i, j int) bool {
		if report.Edges[i].From != report.Edges[j].From { return report.Edges[i].From < report.Edges[j].From }
		if report.Edges[i].To != report.Edges[j].To { return report.Edges[i].To < report.Edges[j].To }
		return report.Edges[i].Relation < report.Edges[j].Relation
	})
	report.Limitations = []string{
		"DNS references are extracted from declared Pod container configuration; this is not runtime DNS query history.",
		"External DNS names are not resolved from the KTRX host because that would not represent the Pod's network or resolver view.",
		"Service-to-Pod edges represent selector matches; EndpointSlice readiness and NetworkPolicy reachability are not evaluated by this topology view.",
	}
	if len(pods) == 0 {
		report.Limitations = append(report.Limitations, "No Pods were found in this namespace.")
	}
	return report
}

type topologyDNSReference struct { Host string; Sources []string }

func podDNSReferences(pod corev1.Pod) []topologyDNSReference {
	sources := map[string]map[string]bool{}
	addText := func(text, source string) {
		for _, host := range topologyHostPattern.FindAllString(text, -1) {
			host = strings.TrimSuffix(strings.ToLower(host), ".")
			if net.ParseIP(host) != nil {
				continue
			}
			if sources[host] == nil { sources[host] = map[string]bool{} }
			sources[host][source] = true
		}
	}
	inspect := func(c corev1.Container, prefix string) {
		for _, env := range c.Env {
			key := strings.ToUpper(env.Name)
			if strings.Contains(key, "HOST") || strings.Contains(key, "URL") || strings.Contains(key, "ENDPOINT") || strings.Contains(key, "DNS") || strings.Contains(key, "ADDR") {
				addText(env.Value, prefix+" env "+env.Name)
			}
		}
		for _, value := range append(append([]string{}, c.Command...), c.Args...) {
			if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
				addText(parsed.Hostname(), prefix+" command/args")
			} else {
				addText(value, prefix+" command/args")
			}
		}
	}
	for _, c := range pod.Spec.InitContainers { inspect(c, "init container "+c.Name) }
	for _, c := range pod.Spec.Containers { inspect(c, "container "+c.Name) }
	for _, c := range pod.Spec.EphemeralContainers {
		for _, value := range c.Command { addText(value, "ephemeral container "+c.Name) }
		for _, value := range c.Args { addText(value, "ephemeral container "+c.Name) }
	}
	for _, alias := range pod.Spec.HostAliases {
		for _, host := range alias.Hostnames {
			addText(host, "hostAliases")
		}
	}
	out := make([]topologyDNSReference, 0, len(sources))
	for host, sourceSet := range sources {
		list := make([]string, 0, len(sourceSet))
		for source := range sourceSet { list = append(list, source) }
		sort.Strings(list)
		out = append(out, topologyDNSReference{Host: host, Sources: list})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func podReadyForTopology(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady { return condition.Status == corev1.ConditionTrue }
	}
	return false
}

func topologyHasNode(nodes []topologyNode, id string) bool {
	for _, node := range nodes { if node.ID == id { return true } }
	return false
}

func printTopologyTerminal(w io.Writer, report topologyReport, colorEnabled bool) {
	style := func(color, value string) string {
		if !colorEnabled { return value }
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
	}
	fmt.Fprintf(w, "%s  namespace/%s\n", style("39", "◆ KTRX TOPOLOGY"), report.Namespace)
	fmt.Fprintf(w, "Generated: %s\nNodes: %d  Relationships: %d\n", report.GeneratedAt, len(report.Nodes), len(report.Edges))
	fmt.Fprintln(w, "\nNODES")
	for _, node := range report.Nodes {
		line := fmt.Sprintf("  [%s] %s", node.Kind, node.Name)
		if node.Address != "" { line += "  address="+node.Address }
		if node.Status != "" { line += "  status="+node.Status }
		fmt.Fprintln(w, style("39", line))
		if len(node.Details) > 0 {
			keys := make([]string, 0, len(node.Details))
			for key := range node.Details { keys = append(keys, key) }
			sort.Strings(keys)
			for _, key := range keys {
				if node.Details[key] != "" { fmt.Fprintf(w, "      %s: %s\n", key, node.Details[key]) }
			}
		}
	}
	fmt.Fprintln(w, "\nRELATIONSHIPS")
	if len(report.Edges) == 0 { fmt.Fprintln(w, "  No relationships discovered from available configuration.") }
	for _, edge := range report.Edges {
		fmt.Fprintf(w, "  %s --(%s)--> %s\n      evidence: %s\n", edge.From, edge.Relation, edge.To, edge.Evidence)
	}
	fmt.Fprintln(w, "\nLIMITATIONS")
	for _, note := range report.Limitations { fmt.Fprintf(w, "  • %s\n", note) }
}

func printTopologyMarkdown(w io.Writer, report topologyReport) error {
	fmt.Fprintf(w, "# KTRX namespace topology: %s\n\nGenerated: %s\n\n", report.Namespace, report.GeneratedAt)
	fmt.Fprintln(w, "## Nodes")
	for _, node := range report.Nodes {
		fmt.Fprintf(w, "- **%s** `%s` — %s; address: %s\n", node.Kind, node.Name, value(node.Status), value(node.Address))
	}
	fmt.Fprintln(w, "\n## Relationships")
	for _, edge := range report.Edges {
		fmt.Fprintf(w, "- `%s` — **%s** → `%s` (%s)\n", edge.From, edge.Relation, edge.To, edge.Evidence)
	}
	fmt.Fprintln(w, "\n## Limitations")
	for _, note := range report.Limitations { fmt.Fprintf(w, "- %s\n", note) }
	return nil
}


func appendCommaValue(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + ", " + next
}
