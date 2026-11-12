package cli

import (
	"errors"
	"testing"

	"github.com/Harry10799/ktrx/internal/analyzer"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", err: nil, want: 0},
		{name: "execution error", err: errors.New("kubernetes unavailable"), want: 1},
		{name: "confirmed failure", err: verdictExitError("FAILED"), want: 2},
		{name: "warning", err: verdictExitError("WARN"), want: 2},
		{name: "unknown", err: verdictExitError("UNKNOWN"), want: 3},
		{name: "wrapped diagnostic error", err: errors.Join(errors.New("context"), verdictExitError("UNKNOWN")), want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.want {
				t.Fatalf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPodReportsExitError(t *testing.T) {
	tests := []struct {
		name    string
		reports []*analyzer.PodReport
		want    int
	}{
		{name: "healthy", reports: []*analyzer.PodReport{{Verdict: analyzer.Healthy}}, want: 0},
		{name: "unknown", reports: []*analyzer.PodReport{{Verdict: analyzer.Unknown}}, want: 3},
		{name: "warning", reports: []*analyzer.PodReport{{Verdict: analyzer.Warning}}, want: 2},
		{name: "failure wins over unknown", reports: []*analyzer.PodReport{{Verdict: analyzer.Unknown}, {Verdict: analyzer.Failed}}, want: 2},
		{name: "unknown takes precedence over warning", reports: []*analyzer.PodReport{{Verdict: analyzer.Warning}, {Verdict: analyzer.Unknown}}, want: 3},
		{name: "unknown when no confirmed findings", reports: []*analyzer.PodReport{{Verdict: analyzer.Healthy}, {Verdict: analyzer.Unknown}}, want: 3},
		{name: "nil report ignored", reports: []*analyzer.PodReport{nil, {Verdict: analyzer.Healthy}}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(podReportsExitError(tt.reports)); got != tt.want {
				t.Fatalf("podReportsExitError() exit code = %d, want %d", got, tt.want)
			}
		})
	}
}
