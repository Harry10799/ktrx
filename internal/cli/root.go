package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
				return printTerminal(report)
			default:
				return fmt.Errorf("unsupported output %q (choose terminal, json or markdown)", output)
			}
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Pod namespace")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	return cmd
}

func printTerminal(r *analyzer.PodReport) error {
	fmt.Printf("kubetrace  %s/%s\\n", r.Namespace, r.Name)
	fmt.Printf("Verdict: %s\\nPhase: %s  Ready: %t  Restarts: %d\\n", r.Verdict, r.Phase, r.Ready, r.Restarts)
	fmt.Printf("Node: %s  Pod IP: %s  QoS: %s  Age: %s\\n", value(r.Node), value(r.PodIP), value(r.QOSClass), r.Age)
	if len(r.OwnerChain) > 0 {
		fmt.Printf("Owner: %s\\n", strings.Join(r.OwnerChain, " → "))
	}
	fmt.Println("\\nContainers:")
	for _, c := range r.Containers {
		fmt.Printf("  • %s  image=%s  state=%s  ready=%t  restarts=%d\\n", c.Name, c.Image, c.State, c.Ready, c.RestartCount)
	}
	fmt.Println("\\nInbound Services:")
	if len(r.InboundServices) == 0 {
		fmt.Println("  • No matching Services")
	}
	for _, svc := range r.InboundServices {
		fmt.Printf("  • %s  type=%s  endpoint=%t  ready=%t  source=%s  ports=%s\\n",
			svc.Name, svc.Type, svc.EndpointFound, svc.EndpointReady, value(svc.EndpointSource), strings.Join(svc.Ports, ", "))
	}
	fmt.Println("\\nEvidence:")
	for _, e := range r.Evidence {
		fmt.Printf("  [%s] %s: %s\\n", e.Verdict, e.Check, e.Summary)
		if e.Suggestion != "" {
			fmt.Printf("      Suggestion: %s\\n", e.Suggestion)
		}
	}
	return nil
}

func printMarkdown(r *analyzer.PodReport) error {
	fmt.Printf("# Pod trace: %s/%s\\n\\n**Verdict:** %s  \\n**Phase:** %s  \\n**Ready:** %t  \\n**Restarts:** %d\\n\\n",
		r.Namespace, r.Name, r.Verdict, r.Phase, r.Ready, r.Restarts)
	fmt.Println("## Containers")
	for _, c := range r.Containers {
		fmt.Printf("- **%s** — %s; state: %s; ready: %t; restarts: %d\\n", c.Name, c.Image, c.State, c.Ready, c.RestartCount)
	}
	fmt.Println("\\n## Inbound Services")
	if len(r.InboundServices) == 0 {
		fmt.Println("- No matching Services")
	}
	for _, svc := range r.InboundServices {
		fmt.Printf("- **%s** (%s): endpoint found=%t, ready=%t, source=%s; ports: %s\\n",
			svc.Name, svc.Type, svc.EndpointFound, svc.EndpointReady, value(svc.EndpointSource), strings.Join(svc.Ports, ", "))
	}
	fmt.Println("\\n## Evidence")
	for _, e := range r.Evidence {
		fmt.Printf("- **%s — %s:** %s\\n", e.Verdict, e.Check, e.Summary)
		if e.Suggestion != "" {
			fmt.Printf("  - Suggested fix: %s\\n", e.Suggestion)
		}
	}
	return nil
}

func value(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
