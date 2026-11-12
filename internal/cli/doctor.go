package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type accessCheck struct {
	Name        string `json:"name"`
	Verb        string `json:"verb"`
	Group       string `json:"group,omitempty"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
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
	optional bool
}

func newDoctorCommand() *cobra.Command {
	var namespace, kubeconfig, contextName, output string
	var timeout time.Duration
	var noColor, includeNetcheck bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check cluster connectivity and the RBAC permissions KTRX needs",
		Long:  "Run a read-only preflight against the selected Kubernetes context. The command checks the permissions used by KTRX diagnostics and never changes cluster resources.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(namespace) == "" {
				return fmt.Errorf("namespace must not be empty")
			}
			if timeout <= 0 {
				return fmt.Errorf("timeout must be greater than zero")
			}
			client, err := kube.NewClientWithTimeout(kubeconfig, contextName, timeout)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			report := doctorReport{Namespace: namespace, Ready: true, Checks: make([]accessCheck, 0)}
			requirements := ktrxAccessRequirements(namespace, includeNetcheck)
			version, versionErr := client.Discovery().ServerVersion()
			if versionErr != nil {
				report.Ready = false
				report.Checks = append(report.Checks, accessCheck{Name: "Kubernetes API connectivity", Resource: "discovery", Verb: "get", Status: "UNKNOWN", Reason: versionErr.Error()})
				report.Unknown++
				for _, req := range requirements {
					report.Checks = append(report.Checks, accessCheck{Name: req.name, Verb: req.verb, Group: req.group, Resource: req.resource, Subresource: req.subresource, Namespace: req.namespace, Status: "UNKNOWN", Reason: "Skipped because Kubernetes API connectivity could not be verified", Optional: req.optional})
					report.Unknown++
				}
			} else {
				report.Server = version.GitVersion
				report.Checks = append(report.Checks, accessCheck{Name: "Kubernetes API connectivity", Resource: "discovery", Verb: "get", Status: "PASS", Reason: "Connected to Kubernetes " + version.GitVersion})
				report.Passed++
				for _, req := range requirements {
					check := checkAccess(ctx, client, req)
					report.Checks = append(report.Checks, check)
					switch check.Status {
					case "PASS":
						report.Passed++
					case "DENIED":
						report.Denied++
						if !req.optional { report.Ready = false }
					default:
						report.Unknown++
						if !req.optional { report.Ready = false }
					}
				}
			}

			if strings.EqualFold(output, "json") {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
			} else if strings.EqualFold(output, "terminal") || strings.EqualFold(output, "text") {
				printDoctorTerminal(cmd, report, !noColor && os.Getenv("NO_COLOR") == "")
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
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	cmd.Flags().BoolVar(&includeNetcheck, "include-netcheck", false, "Also check pods/exec permission required by netcheck")
	return cmd
}

func ktrxAccessRequirements(namespace string, includeNetcheck bool) []accessRequirement {
	requirements := []accessRequirement{
		{name: "Read Pods", verb: "get", resource: "pods", namespace: namespace},
		{name: "List Pods for Deployment inspection", verb: "list", resource: "pods", namespace: namespace},
		{name: "List Services", verb: "list", resource: "services", namespace: namespace},
		{name: "List EndpointSlices", verb: "list", group: "discovery.k8s.io", resource: "endpointslices", namespace: namespace},
		{name: "Read legacy Endpoints fallback", verb: "get", resource: "endpoints", namespace: namespace},
		{name: "List Ingresses", verb: "list", group: "networking.k8s.io", resource: "ingresses", namespace: namespace},
		{name: "List Ingresses across namespaces (--all-namespaces, optional)", verb: "list", group: "networking.k8s.io", resource: "ingresses", optional: true},
		{name: "List Gateway API HTTPRoutes (optional)", verb: "list", group: "gateway.networking.k8s.io", resource: "httproutes", namespace: namespace, optional: true},
		{name: "List Gateway API HTTPRoutes across namespaces (optional)", verb: "list", group: "gateway.networking.k8s.io", resource: "httproutes", optional: true},
		{name: "List AWS TargetGroupBindings (optional)", verb: "list", group: "elbv2.k8s.aws", resource: "targetgroupbindings", namespace: namespace, optional: true},
		{name: "List AWS TargetGroupBindings across namespaces (optional)", verb: "list", group: "elbv2.k8s.aws", resource: "targetgroupbindings", optional: true},
		{name: "Read IngressClasses", verb: "get", group: "networking.k8s.io", resource: "ingressclasses"},
		{name: "List NetworkPolicies", verb: "list", group: "networking.k8s.io", resource: "networkpolicies", namespace: namespace},
		{name: "List Events", verb: "list", resource: "events", namespace: namespace},
		{name: "Read container logs", verb: "get", resource: "pods", subresource: "log", namespace: namespace},
		{name: "Read Deployment for timeline", verb: "get", group: "apps", resource: "deployments", namespace: namespace},
		{name: "List Deployments for timeline", verb: "list", group: "apps", resource: "deployments", namespace: namespace},
		{name: "Read ReplicaSets for owner chain", verb: "get", group: "apps", resource: "replicasets", namespace: namespace},
		{name: "List ReplicaSets for timeline", verb: "list", group: "apps", resource: "replicasets", namespace: namespace},
		{name: "Read referenced ConfigMap metadata", verb: "get", resource: "configmaps", namespace: namespace},
		{name: "Read referenced Secret metadata only", verb: "get", resource: "secrets", namespace: namespace},
		{name: "Discover Ingress controller Pods across namespaces", verb: "list", resource: "pods"},
		{name: "List Flux Kustomizations (optional)", verb: "list", group: "kustomize.toolkit.fluxcd.io", resource: "kustomizations", namespace: namespace, optional: true},
		{name: "List Flux Kustomizations in flux-system (optional)", verb: "list", group: "kustomize.toolkit.fluxcd.io", resource: "kustomizations", namespace: "flux-system", optional: true},
		{name: "List Flux HelmReleases (optional)", verb: "list", group: "helm.toolkit.fluxcd.io", resource: "helmreleases", namespace: namespace, optional: true},
		{name: "List Flux HelmReleases in flux-system (optional)", verb: "list", group: "helm.toolkit.fluxcd.io", resource: "helmreleases", namespace: "flux-system", optional: true},
		{name: "Get Flux GitRepositories (optional)", verb: "get", group: "source.toolkit.fluxcd.io", resource: "gitrepositories", namespace: namespace, optional: true},
		{name: "Get Flux GitRepositories in flux-system (optional)", verb: "get", group: "source.toolkit.fluxcd.io", resource: "gitrepositories", namespace: "flux-system", optional: true},
		{name: "List Argo CD Applications (optional)", verb: "list", group: "argoproj.io", resource: "applications", namespace: "argocd", optional: true},
		{name: "List Argo CD Applications in workload namespace (optional)", verb: "list", group: "argoproj.io", resource: "applications", namespace: namespace, optional: true},
	}
	if includeNetcheck {
		requirements = append(requirements, accessRequirement{name: "Execute explicit in-Pod network probes", verb: "create", resource: "pods", subresource: "exec", namespace: namespace})
	}
	return requirements
}

func checkAccess(ctx context.Context, client kubernetes.Interface, req accessRequirement) accessCheck {
	result := accessCheck{Name: req.name, Verb: req.verb, Group: req.group, Resource: req.resource, Subresource: req.subresource, Namespace: req.namespace, Optional: req.optional}
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
		if review.Status.Reason != "" {
			result.Reason = review.Status.Reason
		}
	case review.Status.Denied:
		result.Status = "DENIED"
		result.Reason = review.Status.Reason
		if result.Reason == "" {
			result.Reason = "The API server denied this permission"
		}
	default:
		result.Status = "UNKNOWN"
		result.Reason = review.Status.Reason
		if review.Status.EvaluationError != "" {
			if result.Reason != "" {
				result.Reason += "; "
			}
			result.Reason += review.Status.EvaluationError
		}
		if result.Reason == "" {
			result.Reason = "The authorization response was inconclusive"
		}
	}
	return result
}

func printDoctorTerminal(cmd *cobra.Command, report doctorReport, colorEnabled bool) {
	style := func(color, value string) string {
		if !colorEnabled {
			return value
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  namespace=%s", style("39", "KTRX DOCTOR"), report.Namespace)
	if report.Server != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Kubernetes=%s", report.Server)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	for _, check := range report.Checks {
		color, symbol := "245", "?"
		switch check.Status {
		case "PASS":
			color, symbol = "42", "✓"
		case "DENIED":
			color, symbol = "196", "✖"
		}
		target := check.Resource
		if check.Subresource != "" {
			target += "/" + check.Subresource
		}
		if check.Group != "" {
			target = check.Group + "/" + target
		}
		if check.Namespace != "" {
			target += " (namespace " + check.Namespace + ")"
		} else if check.Resource == "pods" {
			target += " (all namespaces)"
		}
		name := check.Name
		if check.Optional { name += " (optional)" }
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %-8s %-48s %s\n", style(color, symbol), check.Status, name, target)
		if check.Reason != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "      %s\n", check.Reason)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %s  PASS=%d DENIED=%d UNKNOWN=%d\n", style(map[bool]string{true: "42", false: "196"}[report.Ready], map[bool]string{true: "READY", false: "NOT READY"}[report.Ready]), report.Passed, report.Denied, report.Unknown)
	if !report.Ready {
		fmt.Fprintln(cmd.OutOrStdout(), "Grant only the missing read permissions to the KTRX identity, then run doctor again.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "The checked RBAC permissions are available. Runtime connectivity and application health are not validated by this preflight.")
	}
}
