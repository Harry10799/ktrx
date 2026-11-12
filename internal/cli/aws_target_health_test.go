package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

type fakeELBV2TargetHealthClient struct {
	output *elbv2.DescribeTargetHealthOutput
	err    error
	calls  int
	arn    string
}

func (f *fakeELBV2TargetHealthClient) DescribeTargetHealth(_ context.Context, input *elbv2.DescribeTargetHealthInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error) {
	f.calls++
	f.arn = aws.ToString(input.TargetGroupArn)
	return f.output, f.err
}

func TestAWSRegionFromTargetGroupARN(t *testing.T) {
	arn := "arn:aws:elasticloadbalancing:ap-south-1:123456789012:targetgroup/api/abcdef"
	if got := awsRegionFromTargetGroupARN(arn); got != "ap-south-1" {
		t.Fatalf("region = %q, want ap-south-1", got)
	}
	if got := awsRegionFromTargetGroupARN("not-an-arn"); got != "" {
		t.Fatalf("invalid ARN region = %q, want empty", got)
	}
}

func TestDescribeAWSTargetGroupHealth(t *testing.T) {
	client := &fakeELBV2TargetHealthClient{output: &elbv2.DescribeTargetHealthOutput{
		TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
			{
				Target: &elbv2types.TargetDescription{Id: aws.String("10.0.1.10"), Port: aws.Int32(8080), AvailabilityZone: aws.String("ap-south-1a")},
				TargetHealth: &elbv2types.TargetHealth{State: elbv2types.TargetHealthStateEnum("unhealthy"), Reason: elbv2types.TargetHealthReasonEnum("Target.Timeout"), Description: aws.String("Health checks timed out")},
			},
		},
	}}
	got, err := describeAWSTargetGroupHealth(context.Background(), client, "arn:aws:elasticloadbalancing:ap-south-1:123456789012:targetgroup/api/abcdef")
	if err != nil {
		t.Fatalf("describeAWSTargetGroupHealth() error = %v", err)
	}
	if client.calls != 1 || !stringsHasSuffix(client.arn, ":targetgroup/api/abcdef") {
		t.Fatalf("unexpected AWS request: calls=%d arn=%q", client.calls, client.arn)
	}
	if got.Status != "AVAILABLE" || len(got.Targets) != 1 {
		t.Fatalf("unexpected target health response: %+v", got)
	}
	target := got.Targets[0]
	if target.ID != "10.0.1.10" || target.Port != 8080 || target.State != "unhealthy" || target.Reason != "Target.Timeout" {
		t.Fatalf("target fields not preserved: %+v", target)
	}
}

func TestDescribeAWSTargetGroupHealthReturnsAPIError(t *testing.T) {
	client := &fakeELBV2TargetHealthClient{err: errors.New("AccessDenied")}
	if _, err := describeAWSTargetGroupHealth(context.Background(), client, "arn:aws:elasticloadbalancing:ap-south-1:123456789012:targetgroup/api/abcdef"); err == nil || err.Error() != "AccessDenied" {
		t.Fatalf("expected API error to be preserved, got %v", err)
	}
}

func TestCorrelateAWSTargetsWithPodIPs(t *testing.T) {
	targets := []urlTraceAWSTarget{{ID: "10.0.1.10"}, {ID: "i-0123456789abcdef0"}}
	pods := []urlTracePod{{Name: "api-0", IPs: []string{"10.0.1.10", "fd00::10"}}}
	correlateAWSTargetsWithPods(targets, pods)
	if targets[0].KubernetesPod != "api-0" {
		t.Fatalf("IP target was not correlated with Pod: %+v", targets[0])
	}
	if targets[1].KubernetesPod != "" {
		t.Fatalf("instance ID must not be mistaken for a Pod IP: %+v", targets[1])
	}
}

func TestApplyAWSTargetHealthVerdict(t *testing.T) {
	tests := []struct {
		name    string
		targets []urlTraceAWSTarget
		want    string
	}{
		{name: "all healthy preserves current verdict", targets: []urlTraceAWSTarget{{State: "healthy"}}, want: "HEALTHY"},
		{name: "mixed targets warn", targets: []urlTraceAWSTarget{{State: "healthy"}, {State: "unhealthy"}}, want: "WARN"},
		{name: "all unhealthy fail", targets: []urlTraceAWSTarget{{State: "unhealthy"}, {State: "unhealthy"}}, want: "FAILED"},
		{name: "initial state is not a failure", targets: []urlTraceAWSTarget{{State: "initial"}}, want: "HEALTHY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := urlTraceReport{Verdict: "HEALTHY", Routes: []urlTraceRoute{{Backends: []urlTraceBackend{{AWSTargetGroups: []urlTraceAWSTargetGroupHealth{{Targets: tt.targets}}}}}}}
			applyAWSTargetHealthVerdict(&report)
			if report.Verdict != tt.want {
				t.Fatalf("verdict = %s, want %s", report.Verdict, tt.want)
			}
		})
	}
}

func stringsHasSuffix(value, suffix string) bool {
	return len(value) >= len(suffix) && value[len(value)-len(suffix):] == suffix
}
