package main

import "testing"

func TestParseConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    config
		wantErr bool
	}{
		{
			name: "defaults",
			want: config{addr: ":8080", dataPath: "data", maxBodyBytes: defaultMaxBodyBytes},
		},
		{
			name: "custom",
			args: []string{"-addr", "127.0.0.1:9000", "-data", "/tmp/kantan", "-max-body-bytes", "2048"},
			want: config{addr: "127.0.0.1:9000", dataPath: "/tmp/kantan", maxBodyBytes: 2048},
		},
		{name: "empty address", args: []string{"-addr", ""}, wantErr: true},
		{name: "empty data path", args: []string{"-data", ""}, wantErr: true},
		{name: "zero body limit", args: []string{"-max-body-bytes", "0"}, wantErr: true},
		{name: "unexpected argument", args: []string{"extra"}, wantErr: true},
		{name: "unknown flag", args: []string{"-unknown"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseConfig(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseConfig() error = nil, want error")
				}

				return
			}
			if err != nil {
				t.Fatalf("parseConfig() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("parseConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
