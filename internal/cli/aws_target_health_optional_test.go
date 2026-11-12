package cli

import "testing"

func TestAWSUnavailableDoesNotChangeKubernetesVerdict(t *testing.T) {
	report := urlTraceReport{
		Verdict: "HEALTHY",
		Routes: []urlTraceRoute{{
			Backends: []urlTraceBackend{{
				AWSTargetGroups: []urlTraceAWSTargetGroupHealth{{
					Status: "UNAVAILABLE",
					Error:  "AccessDenied: missing elasticloadbalancing:DescribeTargetHealth",
				}},
			}},
		}},
	}
	applyAWSTargetHealthVerdict(&report)
	if report.Verdict != "HEALTHY" {
		t.Fatalf("optional AWS API failure changed Kubernetes verdict to %s", report.Verdict)
	}
}
