package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestDoctorIncludesTimelineAndOptionalGitOpsPermissions(t *testing.T) {
	requirements := ktrxAccessRequirements("production", false)
	find := func(group, resource, verb string) (accessRequirement, bool) {
		for _, requirement := range requirements {
			if requirement.group == group && requirement.resource == resource && requirement.verb == verb {
				return requirement, true
			}
		}
		return accessRequirement{}, false
	}
	for _, item := range []struct{ group, resource, verb string }{
		{"apps", "deployments", "get"},
		{"apps", "replicasets", "list"},
		{"", "configmaps", "get"},
		{"", "secrets", "get"},
	} {
		if _, ok := find(item.group, item.resource, item.verb); !ok {
			t.Errorf("missing required timeline permission %s %s %s", item.group, item.resource, item.verb)
		}
	}
	for _, item := range []struct{ group, resource string }{
		{"gateway.networking.k8s.io", "httproutes"},
		{"gateway.networking.k8s.io", "referencegrants"},
		{"elbv2.k8s.aws", "targetgroupbindings"},
	} {
		requirement, ok := find(item.group, item.resource, "list")
		if !ok || !requirement.optional {
			t.Errorf("expected %s %s permission to be optional", item.group, item.resource)
		}
	}
	for _, item := range []struct{ group, resource string }{
		{"kustomize.toolkit.fluxcd.io", "kustomizations"},
		{"helm.toolkit.fluxcd.io", "helmreleases"},
		{"source.toolkit.fluxcd.io", "gitrepositories"},
		{"argoproj.io", "applications"},
	} {
		requirement, ok := find(item.group, item.resource, "list")
		if item.resource == "gitrepositories" {
			requirement, ok = find(item.group, item.resource, "get")
		}
		if !ok || !requirement.optional {
			t.Errorf("expected %s %s permission to be optional", item.group, item.resource)
		}
	}
}

func TestDoctorExitErrorIgnoresOptionalPermissionGaps(t *testing.T) {
	tests := []struct {
		name   string
		report doctorReport
		want   int
	}{
		{
			name: "optional denied does not fail readiness",
			report: doctorReport{Ready: true, Denied: 2, Checks: []accessCheck{
				{Name: "required read", Status: "PASS"},
				{Name: "optional CRD", Status: "DENIED", Optional: true},
				{Name: "optional GitOps", Status: "UNKNOWN", Optional: true},
			}},
			want: 0,
		},
		{
			name: "required denied returns failure",
			report: doctorReport{Ready: false, Checks: []accessCheck{
				{Name: "required read", Status: "DENIED"},
				{Name: "optional CRD", Status: "DENIED", Optional: true},
			}},
			want: 2,
		},
		{
			name: "required unknown returns incomplete",
			report: doctorReport{Ready: false, Checks: []accessCheck{
				{Name: "API connectivity", Status: "UNKNOWN"},
				{Name: "optional CRD", Status: "UNKNOWN", Optional: true},
			}},
			want: 3,
		},
		{
			name: "healthy report returns success",
			report: doctorReport{Ready: true, Checks: []accessCheck{
				{Name: "required read", Status: "PASS"},
			}},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(doctorExitError(tt.report)); got != tt.want {
				t.Fatalf("doctorExitError() = %d, want %d", got, tt.want)
			}
		})
	}
}


func TestDoctorReportOutputAndExitClassification(t *testing.T) {
	tests := []struct {
		name   string
		report doctorReport
		want   int
		status string
	}{
		{
			name: "optional denial remains ready",
			report: doctorReport{Namespace: "production", Ready: true, Denied: 1, Checks: []accessCheck{
				{Name: "Read Pods", Resource: "pods", Verb: "get", Status: "PASS"},
				{Name: "List HTTPRoutes", Resource: "httproutes", Status: "DENIED", Optional: true},
			}},
			want: 0, status: "READY",
		},
		{
			name: "required denial is not ready",
			report: doctorReport{Namespace: "production", Ready: false, Denied: 1, Checks: []accessCheck{
				{Name: "Read Pods", Resource: "pods", Verb: "get", Status: "DENIED"},
			}},
			want: 2, status: "NOT READY",
		},
		{
			name: "required unknown is incomplete",
			report: doctorReport{Namespace: "production", Ready: false, Unknown: 1, Checks: []accessCheck{
				{Name: "Read Pods", Resource: "pods", Verb: "get", Status: "UNKNOWN"},
			}},
			want: 3, status: "NOT READY",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var jsonOutput bytes.Buffer
			if err := json.NewEncoder(&jsonOutput).Encode(tt.report); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(jsonOutput.String(), "\"namespace\":\"production\"") {
				t.Fatalf("JSON output missing namespace: %s", jsonOutput.String())
			}
			var terminal bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&terminal)
			printDoctorTerminal(cmd, tt.report, false)
			if !strings.Contains(terminal.String(), "KTRX DOCTOR") || !strings.Contains(terminal.String(), tt.status) {
				t.Fatalf("terminal output missing expected status: %s", terminal.String())
			}
			if got := ExitCode(doctorExitError(tt.report)); got != tt.want {
				t.Fatalf("doctorExitError() = %d, want %d", got, tt.want)
			}
		})
	}
}
