package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/charmbracelet/lipgloss"

	"github.com/Harry10799/kubetrace/internal/analyzer"
	"github.com/Harry10799/kubetrace/internal/kube"
	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor bool
	root := &cobra.Command{
		Use: "kubetrace [POD|deploy/NAME]",
		Short: "Evidence-based Kubernetes troubleshooting",
		SilenceUsage: true,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 { return cmd.Help() }
			return runTrace(cmd, args[0], namespace, kubeconfig, contextName, output, noColor)
		},
	}
	root.Flags().StringVarP(&namespace, "namespace", "n", "default", "Resource namespace")
	root.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	root.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	root.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	root.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	root.AddCommand(newTraceCommand())
	return root
}


func runTrace(cmd *cobra.Command, target, namespace, kubeconfig, contextName, output string, noColor bool) error {
	client, err := kube.NewClient(kubeconfig, contextName)
	if err != nil { return err }
	podName := target
	if strings.HasPrefix(target, "deploy/") || strings.HasPrefix(target, "deployment/") {
		parts := strings.SplitN(target, "/", 2)
		deployment, getErr := client.AppsV1().Deployments(namespace).Get(cmd.Context(), parts[1], metav1.GetOptions{})
		if getErr != nil { return fmt.Errorf("get deployment %s/%s: %w", namespace, parts[1], getErr) }
		selector, selectorErr := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if selectorErr != nil { return fmt.Errorf("deployment has invalid selector: %w", selectorErr) }
		pods, listErr := client.CoreV1().Pods(namespace).List(cmd.Context(), metav1.ListOptions{LabelSelector: selector.String()})
		if listErr != nil { return fmt.Errorf("list deployment pods: %w", listErr) }
		if len(pods.Items) == 0 { return fmt.Errorf("deployment %s/%s has no matching pods", namespace, parts[1]) }
		sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
		podName = pods.Items[0].Name
		fmt.Fprintf(cmd.ErrOrStderr(), "Resolved %s to Pod %s (selected 1 of %d replicas)\n", target, podName, len(pods.Items))
	}
	report, err := (&analyzer.PodAnalyzer{Client: client}).Analyze(cmd.Context(), namespace, podName)
	if err != nil { return err }
	switch strings.ToLower(output) {
	case "json":
		encoder := json.NewEncoder(os.Stdout); encoder.SetIndent("", "  "); return encoder.Encode(report)
	case "markdown":
		return printMarkdown(report)
	case "terminal", "text":
		return printTerminal(report, !noColor && os.Getenv("NO_COLOR") == "")
	default:
		return fmt.Errorf("unsupported output %q (choose terminal, json or markdown)", output)
	}
}

func newTraceCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor bool
	cmd := &cobra.Command{
		Use: "pod POD|deploy/NAME",
		Aliases: []string{"trace"},
		Short: "Inspect a pod and report its current health evidence",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrace(cmd, args[0], namespace, kubeconfig, contextName, output, noColor)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Pod namespace")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	return cmd
}

func printTerminal(r *analyzer.PodReport, colorEnabled bool) error {
	style := func(color string, bold bool, value string) string {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		if bold { s = s.Bold(true) }
		if !colorEnabled { return value }
		return s.Render(value)
	}
	verdict := func(v analyzer.Verdict) string {
		switch v {
		case analyzer.Healthy: return style("42", true, string(v))
		case analyzer.Failed: return style("196", true, string(v))
		case analyzer.Warning: return style("214", true, string(v))
		default: return style("214", true, string(v))
		}
	}
	boolean := func(v bool) string {
		if v { return style("42", true, "true") }
		return style("196", true, "false")
	}
	section := func(v string) string { return style("99", true, v) }

	fmt.Printf("%s  %s/%s\n", style("39", true, "kubetrace"), r.Namespace, r.Name)
	phase := r.Phase
	if r.Phase == "Running" { phase = style("42", true, phase) } else if r.Phase == "Failed" { phase = style("196", true, phase) } else { phase = style("214", true, phase) }
	fmt.Printf("Verdict: %s\nPhase: %s  Ready: %s  Restarts: %d\n", verdict(r.Verdict), phase, boolean(r.Ready), r.Restarts)
	fmt.Printf("Node: %s  Pod IP: %s  QoS: %s  Age: %s\n", value(r.Node), value(r.PodIP), value(r.QOSClass), r.Age)
	if len(r.OwnerChain) > 0 {
		fmt.Printf("Owner: %s\n", strings.Join(r.OwnerChain, " → "))
	}
	fmt.Println("\n" + section("Containers:"))
	for _, c := range r.Containers {
		fmt.Printf("  • %s  image=%s  state=%s  ready=%s  restarts=%d\n", style("39", true, c.Name), c.Image, c.State, boolean(c.Ready), c.RestartCount)
	}
	fmt.Println("\n" + section("Inbound Services:"))
	if len(r.InboundServices) == 0 {
		fmt.Println("  • No matching Services")
	}
	for _, svc := range r.InboundServices {
		fmt.Printf("  • %s  type=%s  endpoint=%s  ready=%s  source=%s  ports=%s  addresses=%s\n",
			style("39", true, svc.Name), svc.Type, boolean(svc.EndpointFound), (func() string { if !svc.EndpointReadyKnown { return style("214", true, "unknown") }; return boolean(svc.EndpointReady) })(), value(svc.EndpointSource), strings.Join(svc.Ports, ", "), strings.Join(svc.EndpointAddresses, ", "))
	}
	fmt.Println("\n" + section("Ingress Routes:"))
	if len(r.IngressRoutes) == 0 { fmt.Println("  • No Ingress routes reference matching Services") }
	for _, route := range r.IngressRoutes {
		fmt.Printf("  • %s  host=%s  path=%s  service=%s:%s  class=%s\n", style("39", true, route.Name), route.Host, route.Path, route.Service, route.ServicePort, value(route.Class))
	}
	fmt.Println("\n" + section("Events Timeline:"))
	if len(r.Events) == 0 { fmt.Println("  • No events found for this Pod") }
	for _, event := range r.Events {
		color := "214"
		if strings.EqualFold(event.Type, "Warning") { color = "196" } else if strings.EqualFold(event.Type, "Normal") { color = "42" }
		fmt.Printf("  • %s  %s  count=%d  %s  %s\n", style(color, true, value(event.Type)), event.Timestamp, event.Count, event.Reason, event.Message)
	}
	fmt.Println("\n" + section("Container Logs (last 20 lines):"))
	for _, logs := range r.Logs {
		fmt.Printf("  %s\n", style("39", true, logs.Name))
		printLogLines("    current: ", logs.Current, logs.CurrentError)
		if logs.Previous != "" || logs.PreviousError != "" { printLogLines("    previous: ", logs.Previous, logs.PreviousError) }
	}
	fmt.Println("\n" + section("Evidence:"))
	for _, e := range r.Evidence {
		fmt.Printf("  [%s] %s: %s\n", verdict(e.Verdict), e.Check, e.Summary)
		if e.Suggestion != "" {
			fmt.Printf("      %s %s\n", style("214", true, "Suggestion:"), e.Suggestion)
		}
	}
	return nil
}

func printMarkdown(r *analyzer.PodReport) error {
	fmt.Printf("# Pod trace: %s/%s\n\n**Verdict:** %s  \n**Phase:** %s  \n**Ready:** %t  \n**Restarts:** %d\n\n",
		r.Namespace, r.Name, r.Verdict, r.Phase, r.Ready, r.Restarts)
	fmt.Println("## Containers")
	for _, c := range r.Containers {
		fmt.Printf("- **%s** — %s; state: %s; ready: %t; restarts: %d\n", c.Name, c.Image, c.State, c.Ready, c.RestartCount)
	}
	fmt.Println("\n## Inbound Services")
	if len(r.InboundServices) == 0 {
		fmt.Println("- No matching Services")
	}
	for _, svc := range r.InboundServices {
		fmt.Printf("- **%s** (%s): endpoint found=%t, ready=%s, source=%s; ports: %s; addresses: %s\n",
			svc.Name, svc.Type, svc.EndpointFound, endpointReadyLabel(svc), value(svc.EndpointSource), strings.Join(svc.Ports, ", "), strings.Join(svc.EndpointAddresses, ", "))
	}
	fmt.Println("\n## Ingress Routes")
	if len(r.IngressRoutes) == 0 { fmt.Println("- No Ingress routes reference matching Services") }
	for _, route := range r.IngressRoutes {
		fmt.Printf("- **%s** — host: %s; path: %s; service: %s:%s; class: %s\n", route.Name, route.Host, route.Path, route.Service, route.ServicePort, value(route.Class))
	}
	fmt.Println("\n## Events Timeline")
	if len(r.Events) == 0 { fmt.Println("- No events found for this Pod") }
	for _, event := range r.Events {
		fmt.Printf("- **%s %s** — %s (count: %d): %s\n", event.Type, event.Timestamp, event.Reason, event.Count, event.Message)
	}
	fmt.Println("\n## Container Logs (last 20 lines)")
	for _, logs := range r.Logs {
		fmt.Printf("\n### %s — current\n\n\x60\x60\x60text\n%s\n\x60\x60\x60\n", logs.Name, logs.Current)
		if logs.CurrentError != "" { fmt.Printf("> Log retrieval error: %s\n", logs.CurrentError) }
		if logs.Previous != "" || logs.PreviousError != "" {
			fmt.Printf("\n### %s — previous\n\n\x60\x60\x60text\n%s\n\x60\x60\x60\n", logs.Name, logs.Previous)
			if logs.PreviousError != "" { fmt.Printf("> Previous log retrieval error: %s\n", logs.PreviousError) }
		}
	}
	fmt.Println("\n## Evidence")
	for _, e := range r.Evidence {
		fmt.Printf("- **%s — %s:** %s\n", e.Verdict, e.Check, e.Summary)
		if e.Suggestion != "" {
			fmt.Printf("  - Suggested fix: %s\n", e.Suggestion)
		}
	}
	return nil
}

func printLogLines(label, content, errText string) {
	if errText != "" {
		fmt.Printf("%s%s\n", label, errText)
		return
	}
	if strings.TrimSpace(content) == "" {
		fmt.Printf("%s<empty>\n", label)
		return
	}
	for i, line := range strings.Split(content, "\n") {
		if i == 0 { fmt.Printf("%s%s\n", label, line) } else { fmt.Printf("%s\n", strings.Repeat(" ", len(label))+line) }
	}
}

func boolLabel(v *bool) string { if v == nil { return "unknown" }; if *v { return "true" }; return "false" }

func endpointReadyLabel(svc analyzer.ServiceTrace) string {
	if !svc.EndpointReadyKnown { return "unknown" }
	if svc.EndpointReady { return "true" }
	return "false"
}

func value(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
