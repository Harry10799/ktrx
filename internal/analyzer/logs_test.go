package analyzer

import "testing"

func TestLogStatusDetectionRequiresExplicitHTTPContext(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantMatch  bool
		wantServer bool
	}{
		{name: "HTTP 503", line: "upstream returned HTTP/1.1 503", wantMatch: true, wantServer: true},
		{name: "nginx access log 404", line: `"GET /x HTTP/1.1" 404 153`, wantMatch: true},
		{name: "nginx access log 502", line: `"POST /api HTTP/1.1" 502 157`, wantMatch: true, wantServer: true},
		{name: "status equals 404", line: "request status=404", wantMatch: true},
		{name: "bare number 500", line: "processed 500 records", wantMatch: false},
		{name: "number embedded in identifier", line: "build-503 completed", wantMatch: false},
		{name: "bare 404", line: "retrying after 404 items", wantMatch: false},
		{name: "explicit server status", line: "response status: 502", wantMatch: true, wantServer: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := noisyLogPattern.MatchString(tt.line); got != tt.wantMatch {
				t.Fatalf("noisyLogPattern.MatchString(%q) = %t, want %t", tt.line, got, tt.wantMatch)
			}
			if got := serverErrorPattern.MatchString(tt.line); got != tt.wantServer {
				t.Fatalf("serverErrorPattern.MatchString(%q) = %t, want %t", tt.line, got, tt.wantServer)
			}
		})
	}
}

func TestAnalyzeLogContentDoesNotFlagBareThreeDigitNumbers(t *testing.T) {
	evidence := analyzeLogContent("app", "processed 500 records\nbuild-503 completed\n")
	if len(evidence) != 1 || evidence[0].Verdict != Healthy {
		t.Fatalf("bare numbers should not create log warnings, got %+v", evidence)
	}
}
