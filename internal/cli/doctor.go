package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/spf13/cobra"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type accessCheck struct {
	Name      string `json:"name"`
	Verb      string `json:"verb"`
	Group     string `json:"group,omitempty"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type doctorReport struct {
	Namespace string        `json:"namespace"`
	Server    string        `json:"serverVersion,omitempty"`
	Ready     bool          `json:"ready"`
	Checks    []accessCheck `json:"checks"`
	Passed    int           `json:"passed"`
	Denied    int           `json:"denied"`
	Unknown   int           `json:"unknown"`
}

type accessRequirement struct {
	name, verb, group, resource, namespace, subresource string
}

func newDoctorCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check cluster connectivity and the RBAC permissions KTRX needs",
		Long:  "Run a read-only preflight against the selected Kubernetes context. The command checks the permissions used by KTRX diagnostics and never changes cluster resources.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := kube.NewClient(kubeconfig, contextName)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			report := doctorReport{Namespace: namespace, Ready: true, Checks: make([]accessCheck, 0)}
			version, versionErr := client.Discovery().ServerVersion()
			if versionErr != nil {
				report.Ready = false
				report.Checks = append(report.Checks, accessCheck{Name: "Kubernetes API connectivity", Resource: "discovery", Verb: "get", Status: "UNKNOWN", Reason: versionErr.Error()})
				report.Unknown++
				for _, req := range ktrxAccessRequirements(namespace) {
					report.Checks = append(report.Checks, accessCheck{Name: req.name, Verb: req.verb, Group: req.group, Resource: req.resource, Namespace: req.namespace, Status: "UNKNOWN", Reason: "Skipped because Kubernetes API connectivity could not be verified"})
					report.Unknown++
				}
			} else {
				report.Server = version.GitVersion
				report.Checks = append(report.Checks, accessCheck{Name: "Kubernetes API connectivity", Resource: "discovery", Verb: "get", Status: "PASS", Reason: "Connected to Kubernetes " + version.GitVersion})
				report.Passed++
				for _, req := range ktrxAccessRequirements(namespace) {
					check := checkAccess(ctx, client, req)
					report.Checks = append(report.Checks, check)
					switch check.Status {
					case "PASS":
						report.Passed++
					case "DENIED":
						report.Denied++
						report.Ready = false
					default:
						report.Unknown++
						report.Ready = false
					}
				}
			}

			if strings.EqualFold(output, "json") {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil { return err }
			} else if strings.EqualFold(output, "terminal") || strings.EqualFold(output, "text") {
				printDoctorTerminal(cmd, report, os.Getenv("NO_COLOR") == "")
			} else {
				return fmt.Errorf("unsupported output %q (choose terminal or json)", output)
			}
			if !report.Ready {
				return fmt.Errorf("preflight incomplete: %d denied, %d unknown; review the checks above", report.Denied, report.Unknown)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace to check")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal or json")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Overall timeout for the preflight")
	return cmd
}

func ktrxAccessRequirements(namespace string) []accessRequirement {
	return []accessRequirement{
		{name: "Read Pods", verb: "get", resource: "pods", namespace: namespace},
		{name: "List Services", verb: "list", resource: "services", namespace: namespace},
		{name: "List EndpointSlices", verb: "list", group: "discovery.k8s.io", resource: "endpointslices", namespace: namespace},
		{name: "Read legacy Endpoints fallback", verb: "get", resource: "endpoints", namespace: namespace},
		{name: "List Ingresses", verb: "list", group: "networking.k8s.io", resource: "ingresses", namespace: namespace},
		{name: "Read IngressClasses", verb: "get", group: "networking.k8s.io", resource: "ingressclasses"},
		{name: "List NetworkPolicies", verb: "list", group: "networking.k8s.io", resource: "networkpolicies", namespace: namespace},
		{name: "List Events", verb: "list", resource: "events", namespace: namespace},
		{name: "Read container logs", verb: "get", resource: "pods", subresource: "log", namespace: namespace},
		{name: "Read ReplicaSets", verb: "get", group: "apps", resource: "replicasets", namespace: namespace},
		{name: "Discover Ingress controller Pods across namespaces", verb: "list", resource: "pods"},
	}
}

func checkAccess(ctx context.Context, client kubernetes.Interface, req accessRequirement) accessCheck {
	result := accessCheck{Name: req.name, Verb: req.verb, Group: req.group, Resource: req.resource, Namespace: req.namespace}
	attrs := &authv1.ResourceAttributes{Verb: req.verb, Group: req.group, Resource: req.resource, Namespace: req.namespace}
	attrs.Subresource = req.subresource
	review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs},
	}, metav1.CreateOptions{})
	if err != nil {
		result.Status = "UNKNOWN"
		result.Reason = err.Error()
		return result
	}
	switch {
	case review.Status.Allowed:
		result.Status = "PASS"
		if review.Status.Reason != "" { result.Reason = review.Status.Reason }
	case review.Status.Denied:
		result.Status = "DENIED"
		result.Reason = review.Status.Reason
		if result.Reason == "" { result.Reason = "The API server denied this permission" }
	default:
		result.Status = "UNKNOWN"
		result.Reason = review.Status.Reason
		if review.Status.EvaluationError != "" {
			if result.Reason != "" { result.Reason += "; " }
			result.Reason += review.Status.EvaluationError
		}
		if result.Reason == "" { result.Reason = "The authorization response was inconclusive" }
	}
	return result
}

func printDoctorTerminal(cmd *cobra.Command, report doctorReport, colorEnabled bool) {
	style := func(color, value string) string {
		if !colorEnabled { return value }
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  namespace=%s", style("39", "KTRX DOCTOR"), report.Namespace)
	if report.Server != "" { fmt.Fprintf(cmd.OutOrStdout(), "  Kubernetes=%s", report.Server) }
	fmt.Fprintln(cmd.OutOrStdout())
	for _, check := range report.Checks {
		color, symbol := "245", "?"
		switch check.Status {
		case "PASS": color, symbol = "42", "✓"
		case "DENIED": color, symbol = "196", "✖"
		}
		target := check.Resource
		if check.Name == "Read container logs" { target += "/log" }
		if check.Group != "" { target = check.Group + "/" + target }
		if check.Namespace != "" { target += " (namespace " + check.Namespace + ")" } else if check.Resource == "pods" { target += " (all namespaces)" }
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %-8s %-48s %s\n", style(color, symbol), check.Status, check.Name, target)
		if check.Reason != "" { fmt.Fprintf(cmd.OutOrStdout(), "      %s\n", check.Reason) }
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %s  PASS=%d DENIED=%d UNKNOWN=%d\n", style(map[bool]string{true:"42",false:"196"}[report.Ready], map[bool]string{true:"READY",false:"NOT READY"}[report.Ready]), report.Passed, report.Denied, report.Unknown)
	if !report.Ready {
		fmt.Fprintln(cmd.OutOrStdout(), "Grant only the missing read permissions to the KTRX identity, then run doctor again.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "The checked RBAC permissions are available. Runtime connectivity and application health are not validated by this preflight.")
	}
}
