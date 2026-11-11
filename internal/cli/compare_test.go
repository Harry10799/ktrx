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

func TestComparePodsReportsImagesForUnmatchedContainerNames(t *testing.T) {
	reference := &analyzer.PodReport{
		Namespace: "default",
		Name:      "reference",
		Containers: []analyzer.ContainerInfo{
			{Name: "nginx", Image: "nginx:latest"},
		},
	}
	candidate := &analyzer.PodReport{
		Namespace: "default",
		Name:      "candidate",
		Containers: []analyzer.ContainerInfo{
			{Name: "ktrx-compare-candidate", Image: "nginx:1.27"},
		},
	}

	result := comparePods(reference, candidate)
	want := map[string][2]string{
		"nginx.image":                  {"nginx:latest", "<absent>"},
		"ktrx-compare-candidate.image": {"<absent>", "nginx:1.27"},
	}
	for field, values := range want {
		found := false
		for _, difference := range result.Differences {
			if difference.Area == "Containers" && difference.Field == field {
				found = true
				if difference.Reference != values[0] || difference.Candidate != values[1] {
					t.Errorf("%s: got %q -> %q, want %q -> %q", field, difference.Reference, difference.Candidate, values[0], values[1])
				}
			}
		}
		if !found {
			t.Errorf("expected container image difference for %s", field)
		}
	}
}
