package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Harry10799/kubetrace/internal/analyzer"
	"github.com/Harry10799/kubetrace/internal/kube"
	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use: "kubetrace",
		Short: "Evidence-based, pod-centric Kubernetes troubleshooting",
		SilenceUsage: true,
	}
	root.AddCommand(newTraceCommand())
	return root
}

func newTraceCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor bool
	cmd := &cobra.Command{
		Use: "trace POD",
		Short: "Inspect a pod and report its current health evidence",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := kube.NewClient(kubeconfig, contextName)
			if err != nil {
				return err
			}
			report, err := (&analyzer.PodAnalyzer{Client: client}).Analyze(cmd.Context(), namespace, args[0])
			if err != nil {
				return err
			}
			switch strings.ToLower(output) {
			case "json":
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			case "markdown":
				return printMarkdown(report)
			case "terminal", "text":
				return printTerminal(report, !noColor && os.Getenv("NO_COLOR") == "")
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
			style("39", true, svc.Name), svc.Type, boolean(svc.EndpointFound), style("42", svc.EndpointReady, endpointReadyLabel(svc)), value(svc.EndpointSource), strings.Join(svc.Ports, ", "), strings.Join(svc.EndpointAddresses, ", "))
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
	fmt.Println("\n## Evidence")
	for _, e := range r.Evidence {
		fmt.Printf("- **%s — %s:** %s\n", e.Verdict, e.Check, e.Summary)
		if e.Suggestion != "" {
			fmt.Printf("  - Suggested fix: %s\n", e.Suggestion)
		}
	}
	return nil
}

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
