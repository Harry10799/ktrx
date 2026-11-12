package cli

import "testing"

func TestURLHostMatches(t *testing.T) {
	tests := []struct{ pattern, host string; want bool }{
		{"api.example.com", "api.example.com", true},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "a.b.example.com", false},
		{"*.example.com", "example.com", false},
		{"*", "anything.example", true},
	}
	for _, tt := range tests {
		if got := urlHostMatches(tt.pattern, tt.host); got != tt.want {
			t.Errorf("urlHostMatches(%q, %q) = %t, want %t", tt.pattern, tt.host, got, tt.want)
		}
	}
}

func TestURLPathMatches(t *testing.T) {
	tests := []struct{ rule, request, kind string; want bool }{
		{"/", "/anything", "Prefix", true},
		{"/api", "/api/v1", "Prefix", true},
		{"/api", "/apix", "Prefix", false},
		{"/api", "/api", "Exact", true},
		{"/api", "/api/v1", "Exact", false},
		{"/api", "/api/v1", "PathPrefix", true},
	}
	for _, tt := range tests {
		if got := urlPathMatches(tt.rule, tt.request, tt.kind); got != tt.want {
			t.Errorf("urlPathMatches(%q, %q, %q) = %t, want %t", tt.rule, tt.request, tt.kind, got, tt.want)
		}
	}
}

func TestURLGlobMatchALBPaths(t *testing.T) {
	tests := []struct { pattern, request string; want bool }{
		{"/api/*", "/api/v1/users", true},
		{"/*", "/health/ready", true},
		{"/api/?", "/api/x", true},
		{"/api/*", "/apix", false},
	}
	for _, tt := range tests {
		got, err := urlGlobMatch(tt.pattern, tt.request)
		if err != nil { t.Fatalf("urlGlobMatch returned error: %v", err) }
		if got != tt.want { t.Errorf("urlGlobMatch(%q, %q) = %t, want %t", tt.pattern, tt.request, got, tt.want) }
	}
}

func TestURLRoutePrecedence(t *testing.T) {
	exactHost := urlTraceRoute{Host:"api.example.com", Path:"/api", Match:"Prefix"}
	wildcardHost := urlTraceRoute{Host:"*.example.com", Path:"/api/v1", Match:"Exact"}
	longerPrefix := urlTraceRoute{Host:"api.example.com", Path:"/api/v1", Match:"Prefix"}
	exactPath := urlTraceRoute{Host:"api.example.com", Path:"/api", Match:"Exact"}
	if urlRoutePrecedence(exactHost) <= urlRoutePrecedence(wildcardHost) { t.Fatal("exact host should outrank wildcard host") }
	if urlRoutePrecedence(longerPrefix) <= urlRoutePrecedence(exactPath) { t.Fatal("longer path should outrank shorter path before match-type tie-break") }
}
