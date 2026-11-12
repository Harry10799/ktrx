package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Harry10799/ktrx/internal/analyzer"
	"github.com/Harry10799/ktrx/internal/timeline"
)

func TestCLIPrintersWriteToProvidedWriter(t *testing.T) {
	tests := []struct {
		name string
		run  func(*bytes.Buffer)
		want string
	}{
		{
			name: "pod terminal",
			run: func(w *bytes.Buffer) {
				if err := printTerminal(w, &analyzer.PodReport{Namespace: "test", Name: "pod"}, false, false); err != nil {
					t.Fatal(err)
				}
			},
			want: "test/pod",
		},
		{
			name: "pod markdown",
			run: func(w *bytes.Buffer) {
				if err := printMarkdown(w, &analyzer.PodReport{Namespace: "test", Name: "pod"}, false); err != nil {
					t.Fatal(err)
				}
			},
			want: "# Pod trace: test/pod",
		},
		{
			name: "deployment terminal",
			run: func(w *bytes.Buffer) {
				if err := printDeploymentTerminal(w, "demo", []*analyzer.PodReport{{Name: "demo-pod"}}, false); err != nil {
					t.Fatal(err)
				}
			},
			want: "DEPLOYMENT",
		},
		{
			name: "comparison terminal",
			run: func(w *bytes.Buffer) {
				if err := printComparisonTerminal(w, podComparison{Namespace: "test", Candidate: "candidate"}, false); err != nil {
					t.Fatal(err)
				}
			},
			want: "KTRX COMPARE",
		},
		{
			name: "comparison markdown",
			run: func(w *bytes.Buffer) {
				if err := printComparisonMarkdown(w, podComparison{Reference: "good", Candidate: "bad", Namespace: "test"}); err != nil {
					t.Fatal(err)
				}
			},
			want: "# Pod comparison: good vs bad",
		},
		{
			name: "netcheck",
			run: func(w *bytes.Buffer) {
				printNetcheck(w, netcheckReport{Namespace: "test", Pod: "pod"}, false)
			},
			want: "KTRX NETCHECK",
		},
		{
			name: "timeline terminal",
			run: func(w *bytes.Buffer) {
				if err := printTimelineTerminal(w, &timeline.Report{Namespace: "test", Target: "demo"}, false); err != nil {
					t.Fatal(err)
				}
			},
			want: "KTRX CHANGE TIMELINE",
		},
		{
			name: "timeline markdown",
			run: func(w *bytes.Buffer) {
				if err := printTimelineMarkdown(w, &timeline.Report{Namespace: "test", Target: "demo"}); err != nil {
					t.Fatal(err)
				}
			},
			want: "# Change timeline: test/demo",
		},
		{
			name: "url trace terminal",
			run: func(w *bytes.Buffer) {
				printURLTrace(w, urlTraceReport{URL: "https://example.com"})
			},
			want: "KTRX URL TRACE",
		},
		{
			name: "url trace markdown",
			run: func(w *bytes.Buffer) {
				if err := printURLTraceMarkdown(w, urlTraceReport{URL: "https://example.com"}); err != nil {
					t.Fatal(err)
				}
			},
			want: "# KTRX URL trace: https://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			tt.run(&output)
			if !strings.Contains(output.String(), tt.want) {
				t.Fatalf("provided writer did not receive expected output %q; got %q", tt.want, output.String())
			}
		})
	}
}
