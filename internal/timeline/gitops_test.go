package timeline

import "testing"

func TestCommitHashExtractsFluxRevision(t *testing.T) {
	tests := []struct {
		revision string
		want     string
	}{
		{revision: "main@sha1:0123456789abcdef", want: "0123456789abcdef"},
		{revision: "release@sha256:abcdef012345", want: "abcdef012345"},
		{revision: "main@branch", want: ""},
	}
	for _, test := range tests {
		if got := commitHash(test.revision); got != test.want {
			t.Errorf("commitHash(%q) = %q, want %q", test.revision, got, test.want)
		}
	}
}
