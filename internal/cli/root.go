package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"

	"github.com/charmbracelet/lipgloss"

	"github.com/Harry10799/ktrx/internal/analyzer"
	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/Harry10799/ktrx/internal/timeline"
	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor, includeLogs, verbose, all bool
	root := &cobra.Command{
		Use:          "ktrx [POD|deploy/NAME]",
		Short:        "Evidence-based Kubernetes troubleshooting",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runTrace(cmd, args[0], namespace, kubeconfig, contextName, output, noColor, includeLogs, verbose, all)
		},
	}
	root.Flags().StringVarP(&namespace, "namespace", "n", "default", "Resource namespace")
	root.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	root.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	root.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	root.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	root.Flags().BoolVar(&includeLogs, "logs", false, "Show full container logs")
	root.Flags().BoolVar(&verbose, "verbose", false, "Include Normal events")
	root.Flags().BoolVar(&all, "all", false, "Inspect every Pod selected by a Deployment")
	root.AddCommand(newTraceCommand())
	root.AddCommand(newCompareCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newNetcheckCommand())
	root.AddCommand(newTimelineCommand())
	root.AddCommand(newTraceURLCommand())
	return root
}

func runTrace(cmd *cobra.Command, target, namespace, kubeconfig, contextName, output string, noColor, includeLogs, verbose, all bool) error {
	client, err := kube.NewClient(kubeconfig, contextName)
	if err != nil {
		return err
	}
	podNames := []string{}
	if strings.HasPrefix(target, "deploy/") || strings.HasPrefix(target, "deployment/") {
		parts := strings.SplitN(target, "/", 2)
		deployment, getErr := client.AppsV1().Deployments(namespace).Get(cmd.Context(), parts[1], metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get deployment %s/%s: %w", namespace, parts[1], getErr)
		}
		selector, selectorErr := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if selectorErr != nil {
			return fmt.Errorf("deployment has invalid selector: %w", selectorErr)
		}
		pods, listErr := client.CoreV1().Pods(namespace).List(cmd.Context(), metav1.ListOptions{LabelSelector: selector.String()})
		if listErr != nil {
			return fmt.Errorf("list deployment pods: %w", listErr)
		}
		if len(pods.Items) == 0 {
			return fmt.Errorf("deployment %s/%s has no matching pods", namespace, parts[1])
		}
		sort.SliceStable(pods.Items, func(i, j int) bool {
			ready := func(p *corev1.Pod) bool {
				for _, cond := range p.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			}
			if ready(&pods.Items[i]) != ready(&pods.Items[j]) {
				return !ready(&pods.Items[i])
			}
			restarts := func(p *corev1.Pod) int32 {
				var n int32
				for _, s := range p.Status.ContainerStatuses {
					n += s.RestartCount
				}
				return n
			}
			if restarts(&pods.Items[i]) != restarts(&pods.Items[j]) {
				return restarts(&pods.Items[i]) > restarts(&pods.Items[j])
			}
			return pods.Items[i].Name < pods.Items[j].Name
		})
		for _, pod := range pods.Items {
			podNames = append(podNames, pod.Name)
		}
		if !all {
			podNames = podNames[:1]
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Resolved %s to %d of %d Pod(s); prioritizing not-ready Pods and higher restart counts\n", target, len(podNames), len(pods.Items))
	} else {
		if all {
			fmt.Fprintln(cmd.ErrOrStderr(), "Note: --all applies to Deployment targets; inspecting the requested Pod only.")
		}
		podNames = append(podNames, target)
	}
	reports := make([]*analyzer.PodReport, 0, len(podNames))
	for _, name := range podNames {
		r, e := (&analyzer.PodAnalyzer{Client: client, IncludeLogs: includeLogs, VerboseEvents: verbose}).Analyze(cmd.Context(), namespace, name)
		if e != nil {
			return e
		}
		reports = append(reports, r)
	}
	addRecentChangesToPodReports(cmd.Context(), client, reports, target, namespace, kubeconfig, contextName)
	switch strings.ToLower(output) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if all && len(reports) > 1 {
			return enc.Encode(reports)
		}
		return enc.Encode(reports[0])
	case "markdown":
		for i, r := range reports {
			if i > 0 {
				fmt.Print("\n---\n")
			}
			if e := printMarkdown(r, includeLogs); e != nil {
				return e
			}
		}
		return nil
	case "terminal", "text":
		if all && len(reports) > 1 && (strings.HasPrefix(target, "deploy/") || strings.HasPrefix(target, "deployment/")) {
			return printDeploymentTerminal(strings.TrimPrefix(strings.TrimPrefix(target, "deployment/"), "deploy/"), reports, !noColor && os.Getenv("NO_COLOR") == "")
		}
		for i, r := range reports {
			if i > 0 {
				fmt.Printf("\n%s\n", strings.Repeat("─", 72))
			}
			if e := printTerminal(r, !noColor && os.Getenv("NO_COLOR") == "", includeLogs); e != nil {
				return e
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported output %q (choose terminal, json or markdown)", output)
	}
}

func newTraceCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var noColor, includeLogs, verbose, all bool
	cmd := &cobra.Command{
		Use:     "pod POD|deploy/NAME",
		Aliases: []string{"trace"},
		Short:   "Inspect a pod and report its current health evidence",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrace(cmd, args[0], namespace, kubeconfig, contextName, output, noColor, includeLogs, verbose, all)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Pod namespace")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal, json or markdown")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	cmd.Flags().BoolVar(&includeLogs, "logs", false, "Show full container logs")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Include Normal events")
	cmd.Flags().BoolVar(&all, "all", false, "Inspect every Pod selected by a Deployment")
	return cmd
}

func printTerminal(r *analyzer.PodReport, colorEnabled, includeLogs bool) error {
	style := func(color string, bold bool, value string) string {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		if bold {
			s = s.Bold(true)
		}
		if !colorEnabled {
			return value
		}
		return s.Render(value)
	}
	verdict := func(v analyzer.Verdict) string {
		switch v {
		case analyzer.Healthy:
			return style("42", true, string(v))
		case analyzer.Failed:
			return style("196", true, string(v))
		case analyzer.Warning:
			return style("214", true, string(v))
		default:
			return style("245", true, string(v))
		}
	}
	boolean := func(v bool) string {
		if v {
			return style("42", true, "true")
		}
		return style("196", true, "false")
	}
	section := func(s string) string { return style("99", true, s) }
	fmt.Printf("%s  %s/%s\n", style("39", true, "ktrx"), r.Namespace, r.Name)
	fmt.Printf("Verdict: %s (%d warnings, %d not checked, %d failed)\n", verdict(r.Verdict), r.WarningCount, r.UnknownCount, r.FailedCount)
	fmt.Printf("\n%s\n", section("Issues:"))
	n := 0
	for _, e := range r.Evidence {
		if e.Verdict == analyzer.Failed || e.Verdict == analyzer.Warning {
			n++
			fmt.Printf("  • [%s] %s: %s\n", verdict(e.Verdict), e.Check, e.Summary)
			if e.Suggestion != "" {
				fmt.Printf("      Suggestion: %s\n", e.Suggestion)
			}
		}
	}
	if n == 0 {
		fmt.Println("  • No failures or warnings detected")
	}
	fmt.Printf("\n%s\n", section("Info:"))
	n = 0
	for _, e := range r.Evidence {
		if e.Verdict == analyzer.Info {
			n++
			fmt.Printf("  • %s: %s\n", e.Check, e.Summary)
		}
	}
	if n == 0 {
		fmt.Println("  • None")
	}
	fmt.Printf("\n%s\n", section("Not checked:"))
	n = 0
	for _, e := range r.Evidence {
		if e.Verdict == analyzer.Unknown {
			n++
			fmt.Printf("  • %s: %s\n", e.Check, e.Summary)
		}
	}
	if n == 0 {
		fmt.Println("  • None")
	}
	fmt.Printf("\n%s\n", section("Details:"))
	fmt.Printf("  Phase: %s  Ready: %s  Restarts: %d\n", r.Phase, boolean(r.Ready), r.Restarts)
	fmt.Printf("  Node: %s  Pod IP: %s  QoS: %s  Age: %s\n", value(r.Node), value(r.PodIP), value(r.QOSClass), r.Age)
	if len(r.OwnerChain) > 0 {
		fmt.Printf("  Owner: %s\n", strings.Join(r.OwnerChain, " → "))
	}
	fmt.Printf("\n%s\n", section("Containers:"))
	for _, v := range r.Containers {
		fmt.Printf("  • %s image=%s state=%s ready=%s restarts=%d\n", style("39", true, v.Name), v.Image, v.State, boolean(v.Ready), v.RestartCount)
	}
	fmt.Printf("\n%s\n", section("Inbound Services:"))
	if len(r.InboundServices) == 0 {
		fmt.Println("  • No matching Services")
	}
	for _, v := range r.InboundServices {
		fmt.Printf("  • %s type=%s endpoint=%s ready=%s ports=%s\n", style("39", true, v.Name), v.Type, boolean(v.EndpointFound), endpointReadyLabel(v), strings.Join(v.Ports, ", "))
	}
	fmt.Printf("\n%s\n", section("Dependencies:"))
	if len(r.Dependencies) == 0 {
		fmt.Println("  • No explicit same-namespace Service dependencies discovered")
	}
	for _, d := range r.Dependencies {
		fmt.Printf("  • %s [%s] %d/%d backends ready — %s\n", style("39", true, d.Service), verdict(d.Verdict), d.ReadyPods, d.SelectedPods, d.Summary)
		if len(d.References) > 0 {
			fmt.Printf("      Referenced by: %s\n", strings.Join(d.References, ", "))
		}
		if len(d.UnreadyPods) > 0 {
			fmt.Printf("      Unready: %s\n", strings.Join(d.UnreadyPods, ", "))
		}
	}
	fmt.Printf("\n%s\n", section("Ingress Routes:"))
	if len(r.IngressRoutes) == 0 {
		fmt.Println("  • No matching Ingress routes")
	}
	for _, v := range r.IngressRoutes {
		fmt.Printf("  • %s host=%s path=%s service=%s:%s class=%s classExists=%s controller=%d/%d addresses=%s\n", style("39", true, v.Name), v.Host, v.Path, v.Service, v.ServicePort, value(v.Class), boolLabel(v.ClassExists), v.ControllerReady, v.ControllerPods, strings.Join(v.Addresses, ", "))
	}
	fmt.Printf("\n%s\n", section("Outbound / DNS:"))
	fmt.Printf("  DNS policy: %s  hostNetwork: %t\n", value(r.Outbound.DNSPolicy), r.Outbound.HostNetwork)
	if len(r.Outbound.Nameservers) > 0 {
		fmt.Printf("  Nameservers: %s\n", strings.Join(r.Outbound.Nameservers, ", "))
	}
	if len(r.Outbound.Searches) > 0 {
		fmt.Printf("  Search domains: %s\n", strings.Join(r.Outbound.Searches, ", "))
	}
	if len(r.Outbound.Options) > 0 {
		fmt.Printf("  Options: %s\n", strings.Join(r.Outbound.Options, ", "))
	}
	if len(r.Outbound.Policies) == 0 {
		fmt.Println("  • No matching NetworkPolicies")
	}
	for _, p := range r.Outbound.Policies {
		state := "not egress-isolating"
		if p.EgressIsolating {
			state = "egress-isolating"
		}
		fmt.Printf("  • %s (%s, %d rule(s))\n", p.Name, state, p.RuleCount)
		if len(p.Destinations) > 0 {
			fmt.Printf("      To: %s\n", strings.Join(p.Destinations, "; "))
		}
		if len(p.Ports) > 0 {
			fmt.Printf("      Ports: %s\n", strings.Join(p.Ports, ", "))
		}
	}
	fmt.Printf("\n%s\n", section("Events:"))
	if len(r.Events) == 0 {
		if includeLogs {
			fmt.Println("  • No events found")
		} else {
			fmt.Println("  • No Warning events (use --verbose to include Normal events)")
		}
	}
	for _, v := range r.Events {
		fmt.Printf("  • %s %s count=%d %s %s\n", v.Type, v.Timestamp, v.Count, v.Reason, strings.ReplaceAll(v.Message, "bytes", " bytes"))
	}
	fmt.Printf("\n%s\n", section("Recent Deployment Changes:"))
	if r.RecentFailureStartedAt != "" {
		fmt.Printf("  Failure started: %s (current unhealthy evidence)\n", r.RecentFailureStartedAt)
	}
	if len(r.RecentChanges) == 0 {
		fmt.Println("  • No recent changes found")
	} else {
		for _, change := range r.RecentChanges {
			fmt.Printf("  • %s [%s] %s — %s\n", change.Timestamp, change.Type, change.Resource, change.Summary)
		}
	}
	if len(r.RecentCorrelations) > 0 {
		fmt.Println("  Potential correlations (not causation):")
		for _, item := range r.RecentCorrelations {
			fmt.Printf("    #%d %s %s — %s\n", item.Rank, item.Timestamp, item.Resource, item.Summary)
		}
	}
	if r.RecentChangesNote != "" {
		fmt.Printf("  Note: %s\n", r.RecentChangesNote)
	}
	fmt.Printf("\n%s\n", section("Logs:"))
	if !includeLogs {
		fmt.Println("  • Error/non-2xx matches only; use --logs for full logs")
	}
	found := false
	for _, v := range r.Logs {
		if v.Current != "" || v.Previous != "" || v.CurrentError != "" || v.PreviousError != "" {
			found = true
		}
		if v.Current != "" {
			fmt.Printf("  %s current:\n%s\n", v.Name, indent(v.Current, "    "))
		}
		if v.Previous != "" {
			fmt.Printf("  %s previous:\n%s\n", v.Name, indent(v.Previous, "    "))
		}
		if v.CurrentError != "" {
			fmt.Printf("  %s current log error: %s\n", v.Name, v.CurrentError)
		}
		if v.PreviousError != "" {
			fmt.Printf("  %s previous log error: %s\n", v.Name, v.PreviousError)
		}
	}
	if !found {
		if includeLogs {
			fmt.Println("  • No log output")
		} else {
			fmt.Println("  • No error/non-2xx lines detected")
		}
	}
	return nil
}
func printMarkdown(r *analyzer.PodReport, includeLogs bool) error {
	fmt.Printf("# Pod trace: %s/%s\n\n**Verdict:** %s (%d warnings, %d not checked, %d failed)\n\n", r.Namespace, r.Name, r.Verdict, r.WarningCount, r.UnknownCount, r.FailedCount)
	fmt.Println("## Issues")
	for _, e := range r.Evidence {
		if e.Verdict == analyzer.Failed || e.Verdict == analyzer.Warning {
			fmt.Printf("- **%s — %s:** %s\n", e.Verdict, e.Check, e.Summary)
			if e.Suggestion != "" {
				fmt.Printf("  - Suggestion: %s\n", e.Suggestion)
			}
		}
	}
	fmt.Println("\n## Info")
	for _, e := range r.Evidence {
		if e.Verdict == analyzer.Info {
			fmt.Printf("- **%s:** %s\n", e.Check, e.Summary)
		}
	}
	fmt.Println("\n## Not checked")
	for _, e := range r.Evidence {
		if e.Verdict == analyzer.Unknown {
			fmt.Printf("- **%s:** %s\n", e.Check, e.Summary)
		}
	}
	fmt.Println("\n## Details")
	fmt.Printf("- Phase: %s; Ready: %t; Restarts: %d; Node: %s; Pod IP: %s; QoS: %s; Age: %s\n", r.Phase, r.Ready, r.Restarts, value(r.Node), value(r.PodIP), value(r.QOSClass), r.Age)
	fmt.Println("\n### Containers")
	for _, v := range r.Containers {
		fmt.Printf("- **%s** — %s; state: %s; ready: %t; restarts: %d\n", v.Name, v.Image, v.State, v.Ready, v.RestartCount)
	}
	fmt.Println("\n### Inbound Services")
	for _, v := range r.InboundServices {
		fmt.Printf("- **%s** (%s): endpoint=%t, ready=%s; ports: %s\n", v.Name, v.Type, v.EndpointFound, endpointReadyLabel(v), strings.Join(v.Ports, ", "))
	}
	fmt.Println("\n### Dependencies")
	for _, d := range r.Dependencies {
		fmt.Printf("- **%s** — %s (%d/%d ready); references: %s\n", d.Service, d.Summary, d.ReadyPods, d.SelectedPods, strings.Join(d.References, ", "))
	}
	fmt.Println("\n### Ingress Routes")
	fmt.Println("\n### Outbound / DNS")
	fmt.Printf("- DNS policy: %s; hostNetwork: %t\n", value(r.Outbound.DNSPolicy), r.Outbound.HostNetwork)
	for _, p := range r.Outbound.Policies {
		fmt.Printf("- **%s** — egress isolation: %t; rules: %d; destinations: %s; ports: %s\n", p.Name, p.EgressIsolating, p.RuleCount, strings.Join(p.Destinations, ", "), strings.Join(p.Ports, ", "))
	}
	for _, v := range r.IngressRoutes {
		fmt.Printf("- **%s** — host: %s; path: %s; service: %s:%s; class: %s; class exists: %s; controller ready: %d/%d; addresses: %s\n", v.Name, v.Host, v.Path, v.Service, v.ServicePort, value(v.Class), boolLabel(v.ClassExists), v.ControllerReady, v.ControllerPods, strings.Join(v.Addresses, ", "))
	}
	fmt.Println("\n### Events")
	for _, v := range r.Events {
		fmt.Printf("- **%s %s** — %s (count: %d): %s\n", v.Type, v.Timestamp, v.Reason, v.Count, strings.ReplaceAll(v.Message, "bytes", " bytes"))
	}
	fmt.Println("\n### Recent Deployment Changes")
	if r.RecentFailureStartedAt != "" {
		fmt.Printf("- Failure started: %s (current unhealthy evidence)\n", r.RecentFailureStartedAt)
	}
	for _, change := range r.RecentChanges {
		fmt.Printf("- %s [%s] %s — %s\n", change.Timestamp, change.Type, change.Resource, change.Summary)
	}
	if len(r.RecentCorrelations) > 0 {
		fmt.Println("\n#### Potential correlations (not causation)")
		for _, item := range r.RecentCorrelations {
			fmt.Printf("- #%d %s %s — %s\n", item.Rank, item.Timestamp, item.Resource, item.Summary)
		}
	}
	if r.RecentChangesNote != "" {
		fmt.Printf("\n%s\n", r.RecentChangesNote)
	}
	fmt.Println("\n### Logs")
	if !includeLogs {
		fmt.Println("Error/non-2xx matches only; use --logs for full logs.")
	}
	for _, v := range r.Logs {
		if v.Current != "" {
			fmt.Printf("\n#### %s — current\n\n\\x60\\x60\\x60text\n%s\n\\x60\\x60\\x60\n", v.Name, v.Current)
		}
		if v.Previous != "" {
			fmt.Printf("\n#### %s — previous\n\n\\x60\\x60\\x60text\n%s\n\\x60\\x60\\x60\n", v.Name, v.Previous)
		}
	}
	return nil
}
func indent(s, prefix string) string { return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix) }

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
		if i == 0 {
			fmt.Printf("%s%s\n", label, line)
		} else {
			fmt.Printf("%s\n", strings.Repeat(" ", len(label))+line)
		}
	}
}

func boolLabel(v *bool) string {
	if v == nil {
		return "unknown"
	}
	if *v {
		return "true"
	}
	return "false"
}

func endpointReadyLabel(svc analyzer.ServiceTrace) string {
	if !svc.EndpointReadyKnown {
		return "unknown"
	}
	if svc.EndpointReady {
		return "true"
	}
	return "false"
}

func value(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func printDeploymentTerminal(name string, reports []*analyzer.PodReport, colorEnabled bool) error {
	style := func(color, value string) string {
		if !colorEnabled {
			return value
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
	}
	fmt.Printf("%s %s  %s\n", style("39", "◆ KTRX"), style("99", "DEPLOYMENT"), name)
	fmt.Printf("%s %d replicas inspected\n", style("245", "↳"), len(reports))

	common := map[string]bool{}
	for _, e := range reports[0].Evidence {
		common[evidenceKey(e)] = true
	}
	for _, r := range reports[1:] {
		present := map[string]bool{}
		for _, e := range r.Evidence {
			present[evidenceKey(e)] = true
		}
		for k := range common {
			if !present[k] {
				delete(common, k)
			}
		}
	}

	fmt.Printf("\n%s\n", style("99", "SHARED FINDINGS"))
	printed := 0
	for _, e := range reports[0].Evidence {
		if !common[evidenceKey(e)] {
			continue
		}
		if e.Verdict == analyzer.Failed || e.Verdict == analyzer.Warning || e.Verdict == analyzer.Info || e.Verdict == analyzer.Unknown {
			fmt.Printf("  %s  %s: %s\n", deploymentVerdictStyle(style, e.Verdict), e.Check, e.Summary)
			if e.Suggestion != "" {
				fmt.Printf("      ↳ %s\n", e.Suggestion)
			}
			printed++
		}
	}
	if printed == 0 {
		fmt.Println("  ✓ No shared findings")
	}

	if len(reports[0].RecentChanges) > 0 || reports[0].RecentChangesNote != "" {
		fmt.Printf("\n%s\n", style("99", "RECENT DEPLOYMENT CHANGES"))
		for _, change := range reports[0].RecentChanges {
			fmt.Printf("  • %s [%s] %s — %s\n", change.Timestamp, change.Type, change.Resource, change.Summary)
		}
		if reports[0].RecentChangesNote != "" {
			fmt.Printf("  Note: %s\n", reports[0].RecentChangesNote)
		}
		for _, item := range reports[0].RecentCorrelations {
			fmt.Printf("  #%d %s %s — %s (potential correlation, not causation)\n", item.Rank, item.Timestamp, item.Resource, item.Summary)
		}
	}
	for _, r := range reports {
		fmt.Printf("\n%s %s\n", style("39", "POD"), shortPodName(r.Name))
		unique := 0
		for _, e := range r.Evidence {
			if common[evidenceKey(e)] {
				continue
			}
			if e.Verdict == analyzer.Failed || e.Verdict == analyzer.Warning || e.Verdict == analyzer.Info || e.Verdict == analyzer.Unknown {
				fmt.Printf("  %s  %s: %s\n", deploymentVerdictStyle(style, e.Verdict), e.Check, e.Summary)
				unique++
			}
		}
		if unique == 0 {
			fmt.Println("  • No pod-specific differences")
		}
		readyLabel := "NOT READY"
		readyVerdict := analyzer.Failed
		if r.Ready {
			readyLabel, readyVerdict = "READY", analyzer.Healthy
		}
		fmt.Printf("  %s  phase=%s  restarts=%d\n", deploymentVerdictStyle(style, readyVerdict)+" "+readyLabel, r.Phase, r.Restarts)
		fmt.Printf("  Node: %s  IP: %s\n", value(r.Node), value(r.PodIP))
	}
	fmt.Printf("\n%s\n", style("245", "Tip: ktrx POD_NAME -n NAMESPACE --logs shows full per-Pod logs."))
	return nil
}

func deploymentVerdictStyle(style func(string, string) string, v analyzer.Verdict) string {
	color, symbol := "245", "○"
	switch v {
	case analyzer.Healthy:
		color, symbol = "42", "✓"
	case analyzer.Failed:
		color, symbol = "196", "✖"
	case analyzer.Warning:
		color, symbol = "214", "▲"
	case analyzer.Info:
		color, symbol = "39", "ℹ"
	case analyzer.Unknown:
		color, symbol = "245", "?"
	}
	return style(color, symbol+" ["+string(v)+"]")
}

func evidenceKey(e analyzer.Evidence) string {
	return string(e.Verdict) + "|" + e.Check + "|" + e.Summary
}

func shortPodName(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) > 2 {
		return strings.Join(parts[:len(parts)-1], "-") + "-…-" + parts[len(parts)-1]
	}
	return name
}

func addRecentChangesToPodReports(ctx context.Context, client kubernetes.Interface, reports []*analyzer.PodReport, target, namespace, kubeconfig, contextName string) {
	if len(reports) == 0 {
		return
	}
	deploymentName := ""
	if strings.HasPrefix(target, "deploy/") || strings.HasPrefix(target, "deployment/") {
		parts := strings.SplitN(target, "/", 2)
		if len(parts) == 2 {
			deploymentName = parts[1]
		}
	} else {
		for _, owner := range reports[0].OwnerChain {
			if strings.HasPrefix(owner, "Deployment/") {
				deploymentName = strings.TrimPrefix(owner, "Deployment/")
				break
			}
		}
	}
	if deploymentName == "" {
		for _, report := range reports {
			report.RecentChangesNote = "Recent Deployment changes are available only for Pods owned by a Deployment."
		}
		return
	}
	restConfig, err := kube.NewRESTConfig(kubeconfig, contextName, 0)
	if err != nil {
		setRecentChangesNote(reports, "Recent changes unavailable: "+err.Error())
		return
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		setRecentChangesNote(reports, "Recent changes unavailable: could not create Kubernetes dynamic client: "+err.Error())
		return
	}
	metadataClient, err := metadata.NewForConfig(restConfig)
	if err != nil {
		setRecentChangesNote(reports, "Recent changes unavailable: could not create metadata-only client: "+err.Error())
		return
	}
	timelineReport, err := (&timeline.Analyzer{
		Client: client, Dynamic: dynamicClient, Metadata: metadataClient, Since: 24 * time.Hour,
	}).AnalyzeDeployment(ctx, namespace, deploymentName)
	if err != nil {
		setRecentChangesNote(reports, "Recent changes unavailable: "+err.Error())
		return
	}
	events := timelineReport.Events
	if len(events) > 10 {
		events = events[len(events)-10:]
	}
	changes := make([]analyzer.RecentChange, 0, len(events))
	for _, event := range events {
		timestamp := "time unavailable"
		if event.Timestamp != nil {
			timestamp = event.Timestamp.UTC().Format(time.RFC3339)
		}
		summary := event.Action
		if len(event.Changes) > 0 {
			fields := make([]string, 0, len(event.Changes))
			for _, change := range event.Changes {
				fields = append(fields, change.Field)
			}
			summary += " (" + strings.Join(fields, ", ") + ")"
		}
		changes = append(changes, analyzer.RecentChange{
			Timestamp: timestamp, Type: string(event.Type), Resource: event.Resource, Summary: summary,
		})
	}
	correlations := make([]analyzer.RecentCorrelation, 0, len(timelineReport.Correlations))
	for _, item := range timelineReport.Correlations {
		timestamp := "time unavailable"
		if item.Timestamp != nil {
			timestamp = item.Timestamp.UTC().Format(time.RFC3339)
		}
		correlations = append(correlations, analyzer.RecentCorrelation{
			Rank: item.Rank, Timestamp: timestamp, Resource: item.Resource,
			Summary: item.Summary, Disclaimer: item.Disclaimer,
		})
	}
	failureStarted := ""
	if timelineReport.FailureStartedAt != nil {
		failureStarted = timelineReport.FailureStartedAt.UTC().Format(time.RFC3339)
	}
	note := "Timeline window: last 24h."
	if len(timelineReport.Limitations) > 0 {
		limitations := timelineReport.Limitations
		if len(limitations) > 2 {
			limitations = limitations[:2]
		}
		note += " " + strings.Join(limitations, " ")
	}
	for _, report := range reports {
		report.RecentChanges = changes
		report.RecentCorrelations = correlations
		report.RecentFailureStartedAt = failureStarted
		report.RecentChangesNote = note
	}
}

func setRecentChangesNote(reports []*analyzer.PodReport, note string) {
	for _, report := range reports {
		report.RecentChangesNote = note
	}
}
