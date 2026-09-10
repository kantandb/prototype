package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const testCursorID = "01950000-0000-7000-8000-000000000001"

func TestQueryCursorRoundTrip(t *testing.T) {
	t.Parallel()

	value, err := encodeIndexValue("alice@example.com")
	if err != nil {
		t.Fatalf("encodeIndexValue() error = %v", err)
	}
	want := queryCursor{index: "email", value: value, id: testCursorID}

	token, err := encodeQueryCursor(want)
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}
	if strings.Contains(token, "=") {
		t.Errorf("token %q contains padding", token)
	}

	got, err := decodeQueryCursor(token)
	if err != nil {
		t.Fatalf("decodeQueryCursor() error = %v", err)
	}
	if got.index != want.index || !bytes.Equal(got.value, want.value) || got.id != want.id {
		t.Errorf("decodeQueryCursor() = %+v, want %+v", got, want)
	}
}

func TestQueryCursorValidation(t *testing.T) {
	t.Parallel()

	valid := func(version byte, index string, value []byte, id string, tail []byte) string {
		data := []byte{version}
		data = appendPart(data, []byte(index))
		data = appendPart(data, value)
		data = appendPart(data, []byte(id))
		data = append(data, tail...)

		return base64.RawURLEncoding.EncodeToString(data)
	}

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty"},
		{name: "invalid base64", token: "!"},
		{name: "truncated", token: base64.RawURLEncoding.EncodeToString([]byte{1})},
		{name: "unsupported version", token: valid(2, "email", []byte{0x00}, testCursorID, nil)},
		{name: "invalid index", token: valid(1, "Bad", []byte{0x00}, testCursorID, nil)},
		{name: "invalid value", token: valid(1, "email", []byte{0xff}, testCursorID, nil)},
		{name: "invalid id", token: valid(1, "email", []byte{0x00}, "bad", nil)},
		{name: "trailing data", token: valid(1, "email", []byte{0x00}, testCursorID, []byte{0})},
		{name: "oversized", token: strings.Repeat("a", base64.RawURLEncoding.EncodedLen(maxQueryCursorSize)+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := decodeQueryCursor(tt.token); !errors.Is(err, errInvalidQueryCursor) {
				t.Errorf("decodeQueryCursor() error = %v, want %v", err, errInvalidQueryCursor)
			}
		})
	}
}
