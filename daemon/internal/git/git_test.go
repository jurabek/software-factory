package git

import "testing"

func TestValidateTestPatternRecursiveGlobSegments(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{name: "standalone recursive prefix", pattern: "**/*_test.go"},
		{name: "standalone recursive suffix", pattern: "tests/**"},
		{name: "embedded recursive wildcard", pattern: "foo**bar", wantErr: true},
		{name: "prefixed recursive wildcard", pattern: "prefix**", wantErr: true},
		{name: "suffixed recursive wildcard", pattern: "**suffix", wantErr: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateTestPattern(test.pattern)
			if test.wantErr && err == nil {
				t.Fatalf("validateTestPattern(%q) error = nil, want error", test.pattern)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateTestPattern(%q) error = %v, want nil", test.pattern, err)
			}
		})
	}
}
