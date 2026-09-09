package main

import (
	"errors"
	"testing"
)

func TestApplyPatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		document  string
		body      string
		mediaType string
		maxBytes  int64
		want      string
		wantErr   error
	}{
		{
			name:      "merge patch",
			document:  `{"name":"first","nested":{"a":1},"remove":true}`,
			body:      `{"name":"second","nested":{"b":2},"remove":null}`,
			mediaType: mergePatchType,
			maxBytes:  100,
			want:      `{"name":"second","nested":{"a":1,"b":2}}`,
		},
		{
			name:      "JSON patch",
			document:  `{"items":["a","b"],"name":"first"}`,
			body:      `[{"op":"replace","path":"/name","value":"second"},{"op":"remove","path":"/items/0"}]`,
			mediaType: jsonPatchType,
			maxBytes:  100,
			want:      `{"items":["b"],"name":"second"}`,
		},
		{name: "malformed merge patch", document: `{}`, body: `{"open":`, mediaType: mergePatchType, maxBytes: 100, wantErr: errInvalidPatch},
		{name: "malformed JSON patch", document: `{}`, body: `{}`, mediaType: jsonPatchType, maxBytes: 100, wantErr: errInvalidPatch},
		{name: "failed operation", document: `{}`, body: `[{"op":"remove","path":"/missing"}]`, mediaType: jsonPatchType, maxBytes: 100, wantErr: errInvalidPatch},
		{name: "non-object result", document: `{}`, body: `null`, mediaType: mergePatchType, maxBytes: 100, wantErr: errInvalidPatch},
		{name: "result too large", document: `{"a":"1234567890"}`, body: `{"b":"1234567890"}`, mediaType: mergePatchType, maxBytes: 20, wantErr: errBodyTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := applyPatch([]byte(tt.document), []byte(tt.body), tt.mediaType, tt.maxBytes)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("applyPatch() error = %v, want %v", err, tt.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatalf("applyPatch() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("applyPatch() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestParsePatchType(t *testing.T) {
	t.Parallel()

	for _, value := range []string{mergePatchType, jsonPatchType + "; charset=utf-8"} {
		if _, err := parsePatchType(value); err != nil {
			t.Errorf("parsePatchType(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"", "application/json", "text/plain", mergePatchType + "; bad"} {
		if _, err := parsePatchType(value); !errors.Is(err, errInvalidPatch) {
			t.Errorf("parsePatchType(%q) error = %v, want %v", value, err, errInvalidPatch)
		}
	}
}

func FuzzApplyPatch(f *testing.F) {
	f.Add([]byte(`{"value":2}`), mergePatchType)
	f.Add([]byte(`[{"op":"replace","path":"/value","value":2}]`), jsonPatchType)
	f.Add([]byte(`null`), mergePatchType)

	f.Fuzz(func(t *testing.T, body []byte, mediaType string) {
		if mediaType != mergePatchType && mediaType != jsonPatchType {
			return
		}

		result, err := applyPatch([]byte(`{"value":1}`), body, mediaType, 1024)
		if err != nil {
			return
		}
		if _, err := validateDoc(result, 1024); err != nil {
			t.Fatalf("applyPatch() returned invalid document: %v", err)
		}
	})
}
