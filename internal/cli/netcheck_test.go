package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNetcheckReportOutputAndExitClassification(t *testing.T) {
	tests := []struct {
		name    string
		results []netcheckResult
		want    int
		status  string
	}{
		{name: "all probes pass", results: []netcheckResult{{Type: "DNS", Target: "api.default.svc", Status: "PASS"}}, want: 0, status: "PASS"},
		{name: "failed probe takes precedence", results: []netcheckResult{{Type: "DNS", Status: "UNAVAILABLE"}, {Type: "TCP", Status: "FAIL"}}, want: 2, status: "FAIL"},
		{name: "unavailable probe is incomplete", results: []netcheckResult{{Type: "DNS", Status: "UNAVAILABLE"}}, want: 3, status: "UNAVAILABLE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := netcheckReport{Namespace: "production", Pod: "api-0", Container: "api", Results: tt.results}
			var output bytes.Buffer
			if err := json.NewEncoder(&output).Encode(report); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "\"pod\":\"api-0\"") || !strings.Contains(output.String(), tt.status) {
				t.Fatalf("JSON output missing report details: %s", output.String())
			}
			if got := ExitCode(netcheckExitError(report)); got != tt.want {
				t.Fatalf("netcheckExitError() = %d, want %d", got, tt.want)
			}
			var terminal bytes.Buffer
			printNetcheck(&terminal, report, false)
			if !strings.Contains(terminal.String(), "KTRX NETCHECK") || !strings.Contains(terminal.String(), tt.status) {
				t.Fatalf("terminal output missing report details: %s", terminal.String())
			}
		})
	}
}
