package cli

import "testing"

func TestApplyAWSTargetHealthVerdictIgnoresUnselectedRoutes(t *testing.T) {
	report := urlTraceReport{
		Verdict: "HEALTHY",
		Routes: []urlTraceRoute{
			{Selected: true, Backends: []urlTraceBackend{{AWSTargetGroups: []urlTraceAWSTargetGroupHealth{{Targets: []urlTraceAWSTarget{{State: "healthy"}}}}}}},
			{Selected: false, Backends: []urlTraceBackend{{AWSTargetGroups: []urlTraceAWSTargetGroupHealth{{Targets: []urlTraceAWSTarget{{State: "unhealthy"}}}}}}},
		},
	}
	applyAWSTargetHealthVerdict(&report)
	if report.Verdict != "HEALTHY" {
		t.Fatalf("unselected route changed verdict to %s", report.Verdict)
	}
}
