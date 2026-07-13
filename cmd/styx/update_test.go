package main

import "testing"

func TestReleaseDisplayVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "adds prefix", version: "1.2.3", want: "v1.2.3"},
		{name: "keeps prefix", version: "v1.2.3", want: "v1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := releaseDisplayVersion(tt.version); got != tt.want {
				t.Fatalf("releaseDisplayVersion(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}
