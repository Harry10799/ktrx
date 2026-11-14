package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

func TestAWSMockTargetHealthScenario(t *testing.T) {
	var fixture struct {
		TargetGroupARN string `json:"targetGroupArn"`
		Targets []struct {
			ID string `json:"id"`
			Port int32 `json:"port"`
			AvailabilityZone string `json:"availabilityZone"`
			State string `json:"state"`
			Reason string `json:"reason"`
			Description string `json:"description"`
		} `json:"targets"`
		Pods []struct {
			Name string `json:"name"`
			IP string `json:"ip"`
		} `json:"pods"`
		ExpectedVerdict string `json:"expectedVerdict"`
	}
	data, err := os.ReadFile("testdata/aws_target_health_mock.json")
	if err != nil {
		t.Fatalf("read AWS mock fixture: %v", err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode AWS mock fixture: %v", err)
	}

	mockOutput := &elbv2.DescribeTargetHealthOutput{}
	for _, item := range fixture.Targets {
		mockOutput.TargetHealthDescriptions = append(mockOutput.TargetHealthDescriptions, elbv2types.TargetHealthDescription{
			Target: &elbv2types.TargetDescription{
				Id: aws.String(item.ID), Port: aws.Int32(item.Port),
				AvailabilityZone: aws.String(item.AvailabilityZone),
			},
			TargetHealth: &elbv2types.TargetHealth{
				State: elbv2types.TargetHealthStateEnum(item.State),
				Reason: elbv2types.TargetHealthReasonEnum(item.Reason),
				Description: aws.String(item.Description),
			},
		})
	}
	client := &fakeELBV2TargetHealthClient{output: mockOutput}
	health, err := describeAWSTargetGroupHealth(context.Background(), client, fixture.TargetGroupARN)
	if err != nil {
		t.Fatalf("parse mocked DescribeTargetHealth response: %v", err)
	}
	if client.calls != 1 || client.arn != fixture.TargetGroupARN {
		t.Fatalf("mock AWS request mismatch: calls=%d arn=%q", client.calls, client.arn)
	}

	pods := make([]urlTracePod, 0, len(fixture.Pods))
	for _, pod := range fixture.Pods {
		pods = append(pods, urlTracePod{Name: pod.Name, IPs: []string{pod.IP}})
	}
	correlateAWSTargetsWithPods(health.Targets, pods)

	report := urlTraceReport{
		URL: "https://api.example.test/health",
		Namespace: "production",
		Host: "api.example.test",
		Path: "/health",
		Verdict: "HEALTHY",
		Routes: []urlTraceRoute{{
			Kind: "Ingress", Namespace: "production", Name: "api",
			Host: "api.example.test", Path: "/", Match: "Prefix", Selected: true,
			Backends: []urlTraceBackend{{
				Namespace: "production", Service: "api", ServiceStatus: "HEALTHY",
				TargetGroupBindings: []urlTraceTargetGroupBinding{{
					Name: "api", TargetGroupARN: fixture.TargetGroupARN, TargetType: "ip",
				}},
				AWSTargetGroups: []urlTraceAWSTargetGroupHealth{*health},
				Pods: pods,
			}},
		}},
	}
	applyAWSTargetHealthVerdict(&report)
	if report.Verdict != fixture.ExpectedVerdict {
		t.Fatalf("verdict = %s, want %s", report.Verdict, fixture.ExpectedVerdict)
	}
	if len(report.Routes[0].Backends[0].AWSTargetGroups[0].Targets) != 2 {
		t.Fatalf("expected two AWS targets, got %+v", health.Targets)
	}
	for i, target := range report.Routes[0].Backends[0].AWSTargetGroups[0].Targets {
		if target.KubernetesPod != fixture.Pods[i].Name {
			t.Errorf("target %s mapped to Pod %q, want %q", target.ID, target.KubernetesPod, fixture.Pods[i].Name)
		}
	}
	if got := report.Routes[0].Backends[0].AWSTargetGroups[0].Targets[1].Reason; got != "Target.Timeout" {
		t.Errorf("unhealthy reason = %q, want Target.Timeout", got)
	}

	jsonOutput, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal mock report as JSON: %v", err)
	}
	if !strings.Contains(string(jsonOutput), `"verdict":"WARN"`) ||
		!strings.Contains(string(jsonOutput), `"kubernetesPod":"api-7c9d8f6b5d-d3e4f"`) {
		t.Fatalf("JSON report is missing expected verdict or Pod correlation: %s", jsonOutput)
	}

	var terminal bytes.Buffer
	printURLTrace(&terminal, report)
	if !strings.Contains(terminal.String(), "Target.Timeout") ||
		!strings.Contains(terminal.String(), "api-7c9d8f6b5d-a1b2c") ||
		!strings.Contains(terminal.String(), "Verdict: WARN") {
		t.Fatalf("terminal report missing mock AWS evidence:\n%s", terminal.String())
	}
}
