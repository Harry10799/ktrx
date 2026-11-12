package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

func TestCheckAccessSelfSubjectAccessReview(t *testing.T) {
	tests := []struct {
		name       string
		status     authv1.SubjectAccessReviewStatus
		apiErr     error
		wantStatus string
	}{
		{name: "allowed", status: authv1.SubjectAccessReviewStatus{Allowed: true, Reason: "RBAC allowed"}, wantStatus: "PASS"},
		{name: "denied", status: authv1.SubjectAccessReviewStatus{Denied: true, Reason: "RBAC denied"}, wantStatus: "DENIED"},
		{name: "inconclusive", status: authv1.SubjectAccessReviewStatus{EvaluationError: "authorizer unavailable"}, wantStatus: "UNKNOWN"},
		{name: "api error", apiErr: errors.New("authorization API unavailable"), wantStatus: "UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if tt.apiErr != nil {
					return true, nil, tt.apiErr
				}
				return true, &authv1.SelfSubjectAccessReview{Status: tt.status}, nil
			})

			req := accessRequirement{
				name: "Read Pod logs", verb: "get", group: "", resource: "pods",
				namespace: "production", subresource: "log",
			}
			got := checkAccess(context.Background(), client, req)
			if got.Status != tt.wantStatus {
				t.Fatalf("checkAccess() status = %q, want %q (reason: %s)", got.Status, tt.wantStatus, got.Reason)
			}
			if tt.name == "api error" && !strings.Contains(got.Reason, tt.apiErr.Error()) {
				t.Fatalf("API error reason = %q, want it to contain %q", got.Reason, tt.apiErr)
			}
			if tt.name == "inconclusive" && !strings.Contains(got.Reason, "authorizer unavailable") {
				t.Fatalf("inconclusive reason = %q, want evaluation error", got.Reason)
			}

			actions := client.Actions()
			if len(actions) != 1 {
				t.Fatalf("expected one SelfSubjectAccessReview request, got %d", len(actions))
			}
			review, ok := actions[0].(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			if !ok {
				t.Fatalf("request object has type %T, want *SelfSubjectAccessReview", actions[0].(k8stesting.CreateAction).GetObject())
			}
			attrs := review.Spec.ResourceAttributes
			if attrs == nil || attrs.Verb != "get" || attrs.Resource != "pods" || attrs.Subresource != "log" || attrs.Namespace != "production" {
				t.Fatalf("unexpected ResourceAttributes: %#v", attrs)
			}
		})
	}
}
