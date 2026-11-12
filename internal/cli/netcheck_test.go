package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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


func TestNetcheckCommandIntegrationRejectsMultiContainerPod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/production/pods/api-0" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
			"apiVersion":"v1","kind":"Pod",
			"metadata":{"name":"api-0","namespace":"production"},
			"spec":{"containers":[{"name":"api","image":"example/api"},{"name":"sidecar","image":"example/sidecar"}]},
			"status":{"phase":"Running","containerStatuses":[
				{"name":"api","state":{"running":{"startedAt":"2026-01-01T00:00:00Z"}}},
				{"name":"sidecar","state":{"running":{"startedAt":"2026-01-01T00:00:00Z"}}}
			]}
		}`))
	}))
	defer server.Close()

	kubeconfigPath := writeDoctorTestKubeconfig(t, server.URL)
	command := newNetcheckCommand()
	command.SilenceUsage = true
	command.SilenceErrors = true
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--kubeconfig", kubeconfigPath,
		"--namespace", "production",
		"--dns", "api.default.svc.cluster.local",
		"api-0",
	})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "specify --container") {
		t.Fatalf("expected multi-container selection error, got %v", err)
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1; err=%v", got, err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("netcheck should not start probes or print a report without a selected container: %s", stdout.String())
	}
}
