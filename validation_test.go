package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		valid bool
	}{
		{name: "a", valid: true},
		{name: "documents_2-prod", valid: true},
		{name: "a" + strings.Repeat("b", 62), valid: true},
		{name: ""},
		{name: "1database"},
		{name: "Database"},
		{name: "has.dot"},
		{name: "has/slash"},
		{name: "a" + strings.Repeat("b", 63)},
		{name: "é"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateName(tt.name)
			if tt.valid && err != nil {
				t.Errorf("validateName() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, errInvalidName) {
				t.Errorf("validateName() error = %v, want %v", err, errInvalidName)
			}
		})
	}
}

func TestValidateID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{name: "valid variant 8", id: "01950000-0000-7000-8000-000000000001", valid: true},
		{name: "valid variant b", id: "ffffffff-ffff-7fff-bfff-ffffffffffff", valid: true},
		{name: "version 4", id: "01950000-0000-4000-8000-000000000001"},
		{name: "invalid variant", id: "01950000-0000-7000-7000-000000000001"},
		{name: "uppercase", id: "01950000-0000-7000-8000-00000000000A"},
		{name: "bad hex", id: "01950000-0000-7000-8000-00000000000g"},
		{name: "missing hyphens", id: "01950000000070008000000000000001"},
		{name: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateID(tt.id)
			if tt.valid && err != nil {
				t.Errorf("validateID() error = %v", err)
			}
			if !tt.valid && !errors.Is(err, errInvalidID) {
				t.Errorf("validateID() error = %v, want %v", err, errInvalidID)
			}
		})
	}
}

func TestValidateDoc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		max     int64
		want    string
		wantErr error
	}{
		{
			name: "canonical object",
			body: ` { "z": 1, "a": { "b": true } } `,
			max:  100,
			want: `{"a":{"b":true},"z":1}`,
		},
		{
			name: "large integer",
			body: `{"value":9007199254740993}`,
			max:  100,
			want: `{"value":9007199254740993}`,
		},
		{name: "array", body: `[]`, max: 100, wantErr: errInvalidDoc},
		{name: "null", body: `null`, max: 100, wantErr: errInvalidDoc},
		{name: "scalar", body: `true`, max: 100, wantErr: errInvalidDoc},
		{name: "malformed", body: `{"open":`, max: 100, wantErr: errInvalidDoc},
		{name: "invalid UTF-8", body: string([]byte{'{', '"', 0xff, '"', ':', '1', '}'}), max: 100, wantErr: errInvalidDoc},
		{name: "trailing value", body: `{} {}`, max: 100, wantErr: errInvalidDoc},
		{name: "input too large", body: `{}`, max: 1, wantErr: errBodyTooLarge},
		{name: "canonical form too large", body: `{"x":"<"}`, max: 10, wantErr: errBodyTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := validateDoc([]byte(tt.body), tt.max)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("validateDoc() error = %v, want %v", err, tt.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatalf("validateDoc() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("validateDoc() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestReadBody(t *testing.T) {
	t.Parallel()

	body, err := readBody(strings.NewReader("1234"), 4)
	if err != nil {
		t.Fatalf("readBody() error = %v", err)
	}
	if string(body) != "1234" {
		t.Errorf("readBody() = %q, want %q", body, "1234")
	}

	if _, err := readBody(strings.NewReader("12345"), 4); !errors.Is(err, errBodyTooLarge) {
		t.Errorf("readBody() error = %v, want %v", err, errBodyTooLarge)
	}

	wantErr := errors.New("read failed")
	if _, err := readBody(errorReader{err: wantErr}, 4); !errors.Is(err, wantErr) {
		t.Errorf("readBody() error = %v, want wrapped %v", err, wantErr)
	}
}

func TestETag(t *testing.T) {
	t.Parallel()

	var want revision
	for i := range want {
		want[i] = byte(i)
	}

	value := formatETag(want)
	if value != `"000102030405060708090a0b0c0d0e0f"` {
		t.Errorf("formatETag() = %q", value)
	}
	got, err := parseETag(value)
	if err != nil {
		t.Fatalf("parseETag() error = %v", err)
	}
	if got != want {
		t.Errorf("parseETag() = %x, want %x", got, want)
	}

	invalid := []string{
		"",
		"000102030405060708090a0b0c0d0e0f",
		`W/"000102030405060708090a0b0c0d0e0f"`,
		`"000102030405060708090a0b0c0d0e0F"`,
		`"000102030405060708090a0b0c0d0e0"`,
		`"000102030405060708090a0b0c0d0e0g"`,
	}
	for _, input := range invalid {
		if _, err := parseETag(input); !errors.Is(err, errInvalidETag) {
			t.Errorf("parseETag(%q) error = %v, want %v", input, err, errInvalidETag)
		}
	}
}

func TestParseIfMatch(t *testing.T) {
	t.Parallel()

	etag := `"000102030405060708090a0b0c0d0e0f"`
	rev, err := parseETag(etag)
	if err != nil {
		t.Fatalf("parseETag() error = %v", err)
	}

	tests := []struct {
		name    string
		values  []string
		want    matchCond
		wantErr bool
	}{
		{name: "absent"},
		{name: "wildcard", values: []string{" * "}, want: matchCond{set: true, wildcard: true}},
		{name: "revision", values: []string{" " + etag + " "}, want: matchCond{set: true, revision: rev}},
		{name: "empty", values: []string{""}, wantErr: true},
		{name: "weak", values: []string{"W/" + etag}, wantErr: true},
		{name: "list", values: []string{etag + ", " + etag}, wantErr: true},
		{name: "multiple headers", values: []string{etag, etag}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseIfMatch(tt.values)
			if tt.wantErr {
				if !errors.Is(err, errInvalidIfMatch) {
					t.Fatalf("parseIfMatch() error = %v, want %v", err, errInvalidIfMatch)
				}

				return
			}
			if err != nil {
				t.Fatalf("parseIfMatch() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("parseIfMatch() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func FuzzValidateName(f *testing.F) {
	for _, seed := range []string{"a", "db-1", "", "UPPER", "é"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		err := validateName(name)
		if err != nil {
			return
		}
		if len(name) == 0 || len(name) > 63 || name[0] < 'a' || name[0] > 'z' {
			t.Fatalf("validateName(%q) accepted invalid bounds", name)
		}
		for i := 1; i < len(name); i++ {
			if !validNameChar(name[i]) {
				t.Fatalf("validateName(%q) accepted byte %q", name, name[i])
			}
		}
	})
}

func FuzzValidateID(f *testing.F) {
	for _, seed := range []string{
		"01950000-0000-7000-8000-000000000001",
		"01950000-0000-4000-8000-000000000001",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, id string) {
		if validateID(id) != nil {
			return
		}
		if len(id) != 36 || id[14] != '7' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("validateID(%q) accepted invalid ID", id)
		}
	})
}

func FuzzValidateDoc(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"value":1}`),
		[]byte(`[]`),
		[]byte(`{"open":`),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		got, err := validateDoc(body, 1024)
		if err != nil {
			return
		}
		if len(got) > 1024 || !json.Valid(got) || len(got) == 0 || got[0] != '{' {
			t.Fatalf("validateDoc() returned invalid JSON: %q", got)
		}

		again, err := validateDoc(got, 1024)
		if err != nil {
			t.Fatalf("validateDoc(canonical) error = %v", err)
		}
		if !bytes.Equal(again, got) {
			t.Fatalf("canonical JSON changed: %q != %q", again, got)
		}
	})
}

func FuzzParseETag(f *testing.F) {
	f.Add(`"000102030405060708090a0b0c0d0e0f"`)
	f.Add("")
	f.Add(`W/"000102030405060708090a0b0c0d0e0f"`)

	f.Fuzz(func(t *testing.T, value string) {
		rev, err := parseETag(value)
		if err == nil && formatETag(rev) != value {
			t.Fatalf("ETag round trip = %q, want %q", formatETag(rev), value)
		}
	})
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

var _ io.Reader = errorReader{}
