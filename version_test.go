package main

import "testing"

func TestSelectVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		release string
		sha     string
		want    string
	}{
		{name: "release", release: "v1.2.3", sha: "0123456789ab", want: "v1.2.3"},
		{name: "development", sha: "0123456789ab", want: "0123456789ab"},
		{name: "missing", want: unknownVersion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := selectVersion(tt.release, tt.sha); got != tt.want {
				t.Errorf("selectVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
