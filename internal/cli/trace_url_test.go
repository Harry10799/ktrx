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
