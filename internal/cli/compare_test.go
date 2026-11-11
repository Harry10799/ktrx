package cli

import (
	"testing"

	"github.com/Harry10799/ktrx/internal/analyzer"
)

func TestComparePodsIgnoresVolatileLogEvidence(t *testing.T) {
	reference := &analyzer.PodReport{
		Namespace: "default",
		Name:      "reference",
		Evidence: []analyzer.Evidence{
			{Check: "log-analysis/nginx", Verdict: analyzer.Warning, Summary: "Detected 2 suspicious log line(s)"},
			{Check: "pod-readiness", Verdict: analyzer.Healthy, Summary: "Pod Ready condition is True"},
		},
	}
	candidate := &analyzer.PodReport{
		Namespace: "default",
		Name:      "candidate",
		Evidence: []analyzer.Evidence{
			{Check: "log-analysis/nginx", Verdict: analyzer.Healthy, Summary: "No recognized error patterns"},
			{Check: "pod-readiness", Verdict: analyzer.Healthy, Summary: "Pod Ready condition is True"},
		},
	}

	result := comparePods(reference, candidate)
	if len(result.Differences) != 0 {
		t.Fatalf("expected no stable differences, got %#v", result.Differences)
	}
}

func TestComparePodsReportsEvidenceByCheck(t *testing.T) {
	reference := &analyzer.PodReport{
		Namespace: "default",
		Name:      "reference",
		Evidence: []analyzer.Evidence{
			{Check: "probes/nginx", Verdict: analyzer.Warning, Summary: "No readiness probe configured"},
		},
	}
	candidate := &analyzer.PodReport{
		Namespace: "default",
		Name:      "candidate",
		Evidence: []analyzer.Evidence{
			{Check: "probes/nginx", Verdict: analyzer.Healthy, Summary: "Readiness probe configured"},
		},
	}

	result := comparePods(reference, candidate)
	if len(result.Differences) != 1 {
		t.Fatalf("expected one evidence difference, got %#v", result.Differences)
	}
	if result.Differences[0].Area != "Diagnostics" || result.Differences[0].Field != "probes/nginx" {
		t.Fatalf("unexpected evidence difference: %#v", result.Differences[0])
	}
}
