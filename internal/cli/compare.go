package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/Harry10799/ktrx/internal/analyzer"
	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/spf13/cobra"
)

type podDifference struct {
	Area      string `json:"area"`
	Field     string `json:"field"`
	Reference string `json:"reference"`
	Candidate string `json:"candidate"`
}

type podComparison struct {
	Namespace  string           `json:"namespace"`
	Reference  string           `json:"referencePod"`
	Candidate  string           `json:"candidatePod"`
	Differences []podDifference `json:"differences"`
	Summary    string           `json:"summary"`
}

func newCompareCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor bool
	cmd := &cobra.Command{
		Use:   "compare REFERENCE_POD CANDIDATE_POD",
		Short: "Compare a known-good Pod with a Pod you are troubleshooting",
		Long:  "Compare two Pods in the same namespace. Pass the known-good reference Pod first and the candidate Pod second.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := kube.NewClient(kubeconfig, contextName)
			if err != nil { return err }
			reference, err := (&analyzer.PodAnalyzer{Client: client}).Analyze(cmd.Context(), namespace, args[0])
			if err != nil { return fmt.Errorf("analyze reference pod: %w", err) }
			candidate, err := (&analyzer.PodAnalyzer{Client: client}).Analyze(cmd.Context(), namespace, args[1])
			if err != nil { return fmt.Errorf("analyze candidate pod: %w", err) }
			result := comparePods(reference, candidate)
			switch strings.ToLower(output) {
			case "json":
				enc := json.NewEncoder(os.Stdout); enc.SetIndent("", "  ")
				return enc.Encode(result)
			case "markdown":
				return printComparisonMarkdown(result)
			case "terminal", "text":
				return printComparisonTerminal(result, !noColor && os.Getenv("NO_COLOR") == "")
			default:
				return fmt.Errorf("unsupported output %q (choose terminal, json or markdown)", output)
			}
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Pod namespace")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	return cmd
}

func comparePods(reference, candidate *analyzer.PodReport) podComparison {
	result := podComparison{Namespace: reference.Namespace, Reference: reference.Name, Candidate: candidate.Name}
	add := func(area, field, left, right string) {
		if left != right {
			result.Differences = append(result.Differences, podDifference{Area: area, Field: field, Reference: left, Candidate: right})
		}
	}
	add("Health", "phase", reference.Phase, candidate.Phase)
	add("Health", "ready", fmt.Sprint(reference.Ready), fmt.Sprint(candidate.Ready))
	add("Health", "verdict", string(reference.Verdict), string(candidate.Verdict))
	add("Health", "restart count", fmt.Sprint(reference.Restarts), fmt.Sprint(candidate.Restarts))
	add("Health", "QoS class", reference.QOSClass, candidate.QOSClass)
	add("Health", "service account", reference.ServiceAccount, candidate.ServiceAccount)
	add("Pod", "node", reference.Node, candidate.Node)

	containerMap := func(items []analyzer.ContainerInfo) map[string]analyzer.ContainerInfo {
		out := make(map[string]analyzer.ContainerInfo, len(items))
		for _, item := range items { out[item.Name] = item }
		return out
	}
	leftContainers, rightContainers := containerMap(reference.Containers), containerMap(candidate.Containers)
	names := map[string]bool{}
	for name := range leftContainers { names[name] = true }
	for name := range rightContainers { names[name] = true }
	ordered := make([]string, 0, len(names))
	for name := range names { ordered = append(ordered, name) }
	sort.Strings(ordered)
	for _, name := range ordered {
		left, leftOK := leftContainers[name]; right, rightOK := rightContainers[name]
		if !leftOK { add("Containers", name, "<absent>", "present"); continue }
		if !rightOK { add("Containers", name, "present", "<absent>"); continue }
		add("Containers", name+".image", left.Image, right.Image)
		add("Containers", name+".state", left.State+"/"+left.Reason, right.State+"/"+right.Reason)
		add("Containers", name+".ready", fmt.Sprint(left.Ready), fmt.Sprint(right.Ready))
		add("Containers", name+".restarts", fmt.Sprint(left.RestartCount), fmt.Sprint(right.RestartCount))
		add("Containers", name+".requests", canonical(left.Requests), canonical(right.Requests))
		add("Containers", name+".limits", canonical(left.Limits), canonical(right.Limits))
	}

	add("Inbound", "Services", serviceSignature(reference), serviceSignature(candidate))
	add("Inbound", "Ingress routes", ingressSignature(reference), ingressSignature(candidate))
	add("Outbound", "DNS configuration", dnsSignature(reference), dnsSignature(candidate))
	add("Outbound", "NetworkPolicies", policySignature(reference), policySignature(candidate))
	add("Diagnostics", "Evidence", evidenceSignature(reference), evidenceSignature(candidate))

	if len(result.Differences) == 0 {
		result.Summary = "No differences found in the compared fields."
	} else {
		result.Summary = fmt.Sprintf("%d difference(s) found. Review the candidate-only findings and configuration changes first.", len(result.Differences))
	}
	return result
}

func canonical(value any) string {
	if value == nil { return "—" }
	data, err := json.Marshal(value)
	if err != nil { return fmt.Sprint(value) }
	return string(data)
}

func serviceSignature(r *analyzer.PodReport) string {
	items := make([]string, 0, len(r.InboundServices))
	for _, s := range r.InboundServices {
		items = append(items, fmt.Sprintf("%s|%s|%s|%t|%t|%s", s.Name, s.Type, strings.Join(s.Ports, ","), s.EndpointFound, s.EndpointReady, canonical(s.Selector)))
	}
	sort.Strings(items); return strings.Join(items, "; ")
}
func ingressSignature(r *analyzer.PodReport) string {
	items := make([]string, 0, len(r.IngressRoutes))
	for _, v := range r.IngressRoutes { items = append(items, fmt.Sprintf("%s|%s|%s|%s|%s|%s", v.Name, v.Host, v.Path, v.Service, v.ServicePort, v.Class)) }
	sort.Strings(items); return strings.Join(items, "; ")
}
func dnsSignature(r *analyzer.PodReport) string {
	return fmt.Sprintf("policy=%s hostNetwork=%t nameservers=%s searches=%s options=%s", r.Outbound.DNSPolicy, r.Outbound.HostNetwork, strings.Join(r.Outbound.Nameservers, ","), strings.Join(r.Outbound.Searches, ","), strings.Join(r.Outbound.Options, ","))
}
func policySignature(r *analyzer.PodReport) string {
	items := make([]string, 0, len(r.Outbound.Policies))
	for _, p := range r.Outbound.Policies { items = append(items, fmt.Sprintf("%s|%s|%t|%d|%s|%s", p.Name, p.PodSelector, p.EgressIsolating, p.RuleCount, strings.Join(p.Destinations, ","), strings.Join(p.Ports, ","))) }
	sort.Strings(items); return strings.Join(items, "; ")
}
func evidenceSignature(r *analyzer.PodReport) string {
	items := make([]string, 0, len(r.Evidence))
	for _, e := range r.Evidence { items = append(items, fmt.Sprintf("%s|%s|%s", e.Check, e.Verdict, e.Summary)) }
	sort.Strings(items); return strings.Join(items, "; ")
}

func printComparisonTerminal(r podComparison, colorEnabled bool) error {
	style := func(color, value string) string {
		if !colorEnabled { return value }
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
	}
	fmt.Printf("%s  %s/%s\n", style("39", "◆ KTRX COMPARE"), r.Namespace, r.Candidate)
	fmt.Printf("Reference: %s\nCandidate: %s\n", r.Reference, r.Candidate)
	fmt.Println("\n" + style("99", "DIFFERENCES"))
	if len(r.Differences) == 0 { fmt.Println("  ✓ No differences found") }
	for _, d := range r.Differences {
		fmt.Printf("  %s %s / %s\n", style("214", "Δ"), d.Area, d.Field)
		fmt.Printf("      Reference: %s\n      Candidate: %s\n", value(d.Reference), value(d.Candidate))
	}
	fmt.Println("\n" + style("245", r.Summary))
	fmt.Println(style("245", "Note: comparison highlights observed differences; it does not prove causation."))
	return nil
}

func printComparisonMarkdown(r podComparison) error {
	fmt.Printf("# Pod comparison: %s vs %s\n\nNamespace: %s\n\n", r.Reference, r.Candidate, r.Namespace)
	fmt.Println("| Area | Field | Reference | Candidate |")
	fmt.Println("|---|---|---|---|")
	for _, d := range r.Differences {
		fmt.Printf("| %s | %s | %s | %s |\n", d.Area, d.Field, strings.ReplaceAll(d.Reference, "|", "\\|"), strings.ReplaceAll(d.Candidate, "|", "\\|"))
	}
	fmt.Printf("\n**%s**\n\n> Differences are diagnostic clues, not proof of causation.\n", r.Summary)
	return nil
}
