package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/Harry10799/ktrx/internal/timeline"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
)

func newTimelineCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor bool
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "timeline deploy/NAME",
		Short: "Show retained Deployment rollout revisions and template changes",
		Long:  "Build an evidence-based timeline from Deployment-owned ReplicaSets. Only revisions still present in the Kubernetes API are available.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			name := strings.TrimPrefix(strings.TrimPrefix(target, "deployment/"), "deploy/")
			if name == target || name == "" {
				return fmt.Errorf("target must use deploy/NAME or deployment/NAME")
			}
			client, err := kube.NewClient(kubeconfig, contextName)
			if err != nil {
				return err
			}
			restConfig, err := kube.NewRESTConfig(kubeconfig, contextName, 0)
			if err != nil { return err }
			dynamicClient, err := dynamic.NewForConfig(restConfig)
			if err != nil { return fmt.Errorf("create dynamic Kubernetes client: %w", err) }
			metadataClient, err := metadata.NewForConfig(restConfig)
			if err != nil { return fmt.Errorf("create metadata-only Kubernetes client: %w", err) }
			report, err := (&timeline.Analyzer{Client: client, Dynamic: dynamicClient, Metadata: metadataClient, Since: since}).AnalyzeDeployment(cmd.Context(), namespace, name)
			if err != nil {
				return err
			}
			switch strings.ToLower(output) {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			case "markdown":
				return printTimelineMarkdown(report)
			case "terminal", "text":
				return printTimelineTerminal(report, !noColor && os.Getenv("NO_COLOR") == "")
			default:
				return fmt.Errorf("unsupported output %q (choose terminal, json or markdown)", output)
			}
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Deployment namespace")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "Include timeline evidence from this duration (default 24h)")
	return cmd
}

func formatTimelineTime(value *time.Time) string {
	if value == nil {
		return "time unavailable"
	}
	return value.UTC().Format("2006-01-02 15:04:05Z")
}

func printTimelineTerminal(report *timeline.Report, colorEnabled bool) error {
	style := func(color, value string) string {
		if !colorEnabled {
			return value
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
	}
	fmt.Printf("%s  %s\n", style("39", "◆ KTRX CHANGE TIMELINE"), report.Namespace+"/"+report.Target)
	fmt.Printf("Generated: %s\n\n", formatTimelineTime(&report.GeneratedAt))
	if report.FailureStartedAt != nil {
		fmt.Printf("Failure started: %s (earliest retained failure evidence)\n\n", formatTimelineTime(report.FailureStartedAt))
	}
	if len(report.Events) == 0 {
		fmt.Println("No timeline events found.")
	}
	for _, event := range report.Events {
		current := ""
		if event.Current {
			current = " ◀ current"
		}
		fmt.Printf("%s  %s  %s  revision=%s%s\n", formatTimelineTime(event.Timestamp), style("39", string(event.Type)), event.Resource, event.Revision, current)
		fmt.Printf("       %s\n", event.Action)
		if event.Details != "" {
			fmt.Printf("       %s\n", event.Details)
		}
		for _, change := range event.Changes {
			fmt.Printf("       • %s: %s → %s\n", change.Field, change.Before, change.After)
		}
	}
	if len(report.Correlations) > 0 {
		fmt.Printf("\n%s\n", style("214", "POTENTIAL CORRELATIONS (NOT CAUSATION)"))
		for _, item := range report.Correlations {
			fmt.Printf("  #%d %s %s %s\n", item.Rank, formatTimelineTime(item.Timestamp), item.Resource, item.Summary)
		}
	}
	fmt.Printf("\n%s\n", style("99", "LIMITATIONS"))
	for _, limitation := range report.Limitations {
		fmt.Printf("  • %s\n", limitation)
	}
	return nil
}

func printTimelineMarkdown(report *timeline.Report) error {
	fmt.Printf("# Change timeline: %s\n\n", report.Namespace+"/"+report.Target)
	fmt.Printf("Generated: %s\n\n", formatTimelineTime(&report.GeneratedAt))
	if report.FailureStartedAt != nil {
		fmt.Printf("**Failure started:** %s (earliest retained failure evidence)\n\n", formatTimelineTime(report.FailureStartedAt))
	}
	if len(report.Events) == 0 {
		fmt.Println("No timeline events found in the selected window.")
	}
	for _, event := range report.Events {
		current := ""
		if event.Current {
			current = " — CURRENT"
		}
		fmt.Printf("## %s — %s (revision %s)%s\n\n", formatTimelineTime(event.Timestamp), event.Resource, event.Revision, current)
		fmt.Printf("%s\n\n", event.Action)
		for _, change := range event.Changes {
			fmt.Printf("- **%s:** %s → %s\n", change.Field, change.Before, change.After)
		}
		if event.Details != "" {
			fmt.Printf("\n%s\n", event.Details)
		}
		fmt.Println()
	}
	if len(report.Correlations) > 0 {
		fmt.Println("## Potential correlations (not causation)")
		for _, item := range report.Correlations {
			fmt.Printf("%d. %s — %s: %s\n", item.Rank, formatTimelineTime(item.Timestamp), item.Resource, item.Summary)
		}
	}
	fmt.Println("## Limitations")
	for _, limitation := range report.Limitations {
		fmt.Printf("- %s\n", limitation)
	}
	return nil
}
