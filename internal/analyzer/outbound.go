package analyzer

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// OutboundTrace reports declarative egress policy and pod DNS configuration.
// It deliberately does not claim that a destination is reachable: NetworkPolicy
// enforcement and runtime connectivity depend on the CNI and live network state.
type OutboundTrace struct {
	DNSPolicy  string                 `json:"dnsPolicy" yaml:"dnsPolicy"`
	HostNetwork bool                  `json:"hostNetwork" yaml:"hostNetwork"`
	Nameservers []string              `json:"nameservers,omitempty" yaml:"nameservers,omitempty"`
	Searches    []string              `json:"searches,omitempty" yaml:"searches,omitempty"`
	Options     []string              `json:"options,omitempty" yaml:"options,omitempty"`
	Policies    []OutboundPolicyTrace `json:"policies,omitempty" yaml:"policies,omitempty"`
}

type OutboundPolicyTrace struct {
	Name             string   `json:"name" yaml:"name"`
	PodSelector      string   `json:"podSelector" yaml:"podSelector"`
	EgressIsolating  bool     `json:"egressIsolating" yaml:"egressIsolating"`
	RuleCount        int      `json:"ruleCount" yaml:"ruleCount"`
	Destinations     []string `json:"destinations,omitempty" yaml:"destinations,omitempty"`
	Ports            []string `json:"ports,omitempty" yaml:"ports,omitempty"`
}

func traceOutbound(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) (OutboundTrace, []Evidence) {
	dnsPolicy := string(pod.Spec.DNSPolicy)
	if dnsPolicy == "" { dnsPolicy = string(corev1.DNSClusterFirst) }
	trace := OutboundTrace{DNSPolicy: dnsPolicy, HostNetwork: pod.Spec.HostNetwork}
	if pod.Spec.DNSConfig != nil {
		trace.Nameservers = append(trace.Nameservers, pod.Spec.DNSConfig.Nameservers...)
		trace.Searches = append(trace.Searches, pod.Spec.DNSConfig.Searches...)
		for _, option := range pod.Spec.DNSConfig.Options {
			if option != nil {
				value := option.Name
				if option.Value != nil { value += ":" + *option.Value }
				trace.Options = append(trace.Options, value)
			}
		}
	}
	evidence := []Evidence{{
		Check: "dns-config", Verdict: Info,
		Summary: fmt.Sprintf("Pod DNS policy is %s; runtime name resolution has not been tested", dnsPolicy),
		Details: dnsDetails(trace),
		Suggestion: "Use an in-container DNS probe to verify service and external name resolution.",
	}}

	policies, err := client.NetworkingV1().NetworkPolicies(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		evidence = append(evidence, Evidence{Check: "outbound-network-policy", Verdict: Unknown,
			Summary: "Could not list NetworkPolicies to inspect egress restrictions",
			Details: []string{err.Error()},
			Suggestion: "Grant list access to networking.k8s.io NetworkPolicies in the Pod namespace."})
		return trace, evidence
	}

	egressIsolated := false
	for i := range policies.Items {
		policy := &policies.Items[i]
		selector, selectorErr := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if selectorErr != nil || !selector.Matches(labels.Set(pod.Labels)) { continue }
		egress := policyAppliesToEgress(policy)
		item := OutboundPolicyTrace{
			Name: policy.Name, PodSelector: selector.String(),
			EgressIsolating: egress, RuleCount: len(policy.Spec.Egress),
		}
		if egress { egressIsolated = true }
		for _, rule := range policy.Spec.Egress {
			for _, to := range rule.To {
				switch {
				case to.IPBlock != nil:
					item.Destinations = appendUnique(item.Destinations, to.IPBlock.CIDR)
					for _, except := range to.IPBlock.Except { item.Destinations = appendUnique(item.Destinations, "except "+except) }
				case to.NamespaceSelector != nil && to.PodSelector != nil:
					item.Destinations = appendUnique(item.Destinations, "namespaceSelector="+selectorString(to.NamespaceSelector)+", podSelector="+selectorString(to.PodSelector))
				case to.NamespaceSelector != nil:
					item.Destinations = appendUnique(item.Destinations, "namespaceSelector="+selectorString(to.NamespaceSelector))
				case to.PodSelector != nil:
					item.Destinations = appendUnique(item.Destinations, "podSelector="+selectorString(to.PodSelector)+" (same namespace)")
				}
			}
			if len(rule.To) == 0 { item.Destinations = appendUnique(item.Destinations, "all destinations") }
			if len(rule.Ports) == 0 { item.Ports = appendUnique(item.Ports, "all ports/protocols") }
			for _, port := range rule.Ports {
				proto := corev1.ProtocolTCP
				if port.Protocol != nil { proto = *port.Protocol }
				value := string(proto) + "/"
				if port.Port != nil {
					if port.Port.StrVal != "" { value += port.Port.StrVal } else { value += fmt.Sprintf("%d", port.Port.IntVal) }
				} else { value += "all ports" }
				if port.EndPort != nil { value += "-" + fmt.Sprintf("%d", *port.EndPort) }
				item.Ports = appendUnique(item.Ports, value)
			}
		}
		trace.Policies = append(trace.Policies, item)
		verdict := Info
		summary := fmt.Sprintf("NetworkPolicy %s selects this Pod; egress isolation=%t with %d egress rule(s)", policy.Name, egress, len(policy.Spec.Egress))
		if egress && len(policy.Spec.Egress) == 0 {
			verdict = Warning
			summary += "; this policy contributes no allowed egress destinations"
		}
		details := append([]string{}, item.Destinations...)
		details = append(details, item.Ports...)
		evidence = append(evidence, Evidence{Check: "outbound-policy/"+policy.Name, Verdict: verdict, Summary: summary,
			Details: details, Suggestion: "Review all policies selecting this Pod; allowed egress is the union of matching NetworkPolicy rules."})
	}
	if len(trace.Policies) == 0 {
		evidence = append(evidence, Evidence{Check: "outbound-network-policy", Verdict: Info,
			Summary: "No NetworkPolicy in this namespace selects the Pod",
			Suggestion: "This means no namespace NetworkPolicy egress isolation was found; it does not prove end-to-end connectivity."})
	} else if !egressIsolated {
		evidence = append(evidence, Evidence{Check: "outbound-egress-isolation", Verdict: Info,
			Summary: "Matching NetworkPolicies were found, but none applies egress isolation to this Pod"})
	} else {
		evidence = append(evidence, Evidence{Check: "outbound-egress-isolation", Verdict: Info,
			Summary: "This Pod is egress-isolated by at least one matching NetworkPolicy; allowed traffic is the union of matching egress rules"})
	}
	evidence = append(evidence, Evidence{Check: "outbound-connectivity", Verdict: Unknown,
		Summary: "Live outbound connections have not been tested",
		Suggestion: "Use kubetrace netcheck (planned) or an approved in-container probe to test DNS, TCP and HTTP to a named destination."})
	return trace, evidence
}

func policyAppliesToEgress(policy *networkingv1.NetworkPolicy) bool {
	for _, policyType := range policy.Spec.PolicyTypes {
		if policyType == networkingv1.PolicyTypeEgress { return true }
	}
	// API defaulting adds Egress when egress rules are present.
	return len(policy.Spec.Egress) > 0
}

func selectorString(selector *metav1.LabelSelector) string {
	if selector == nil { return "{}" }
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil { return "<invalid selector>" }
	return parsed.String()
}

func dnsDetails(trace OutboundTrace) []string {
	var details []string
	if trace.HostNetwork { details = append(details, "hostNetwork=true") }
	if len(trace.Nameservers) > 0 { details = append(details, "nameservers="+strings.Join(trace.Nameservers, ",")) }
	if len(trace.Searches) > 0 { details = append(details, "searches="+strings.Join(trace.Searches, ",")) }
	if len(trace.Options) > 0 { details = append(details, "options="+strings.Join(trace.Options, ",")) }
	if len(details) == 0 { details = append(details, "DNS settings use Kubernetes/node defaults") }
	return details
}

func appendUnique(items []string, value string) []string {
	if value == "" { return items }
	for _, item := range items { if item == value { return items } }
	return append(items, value)
}
