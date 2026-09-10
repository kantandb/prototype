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
	want := queryCursor{database: "users", index: "email", op: cmpGE, value: value, id: testCursorID}

	token, err := encodeQueryCursor(want)
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}
	if strings.Contains(token, "=") {
		t.Errorf("token %q contains padding", token)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if bytes.Contains(sealed, []byte(want.id)) || bytes.Contains(sealed, want.value) {
		t.Error("token exposes cursor contents")
	}

	got, err := decodeQueryCursor(token)
	if err != nil {
		t.Fatalf("decodeQueryCursor() error = %v", err)
	}
	if got.database != want.database || got.index != want.index || got.op != want.op || !bytes.Equal(got.value, want.value) || got.id != want.id {
		t.Errorf("decodeQueryCursor() = %+v, want %+v", got, want)
	}
}

func TestQueryCursorOperatorMatch(t *testing.T) {
	t.Parallel()

	value, err := encodeIndexValue("alice@example.com")
	if err != nil {
		t.Fatalf("encodeIndexValue() error = %v", err)
	}
	token, err := encodeQueryCursor(queryCursor{database: "users", index: "email", op: cmpLT, value: value, id: testCursorID})
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}

	query := docQuery{index: "email", op: cmpGE, value: "alice@example.com", cursor: token}
	if _, _, err := queryStart("users", query); !errors.Is(err, errInvalidQueryCursor) {
		t.Errorf("queryStart() error = %v, want %v", err, errInvalidQueryCursor)
	}
}

func TestQueryCursorTampering(t *testing.T) {
	t.Parallel()

	token, err := encodeQueryCursor(queryCursor{database: "users", index: "email", op: cmpEq, value: []byte{0x00}, id: testCursorID})
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	sealed[len(sealed)-1] ^= 1
	token = base64.RawURLEncoding.EncodeToString(sealed)

	if _, err := decodeQueryCursor(token); !errors.Is(err, errInvalidQueryCursor) {
		t.Errorf("decodeQueryCursor() error = %v, want %v", err, errInvalidQueryCursor)
	}
}

func TestQueryCursorValidation(t *testing.T) {
	t.Parallel()

	data := func(version byte, database, index string, op byte, value []byte, id string, tail []byte) []byte {
		encoded := []byte{version}
		encoded = appendPart(encoded, []byte(database))
		encoded = appendPart(encoded, []byte(index))
		encoded = append(encoded, op)
		encoded = appendPart(encoded, value)
		encoded = appendPart(encoded, []byte(id))

		return append(encoded, tail...)
	}
	token := func(version byte, database, index string, op byte, value []byte, id string, tail []byte) string {
		return sealQueryCursor(t, data(version, database, index, op, value, id, tail))
	}

	old := []byte{queryCursorVersion - 1}
	old = appendPart(old, []byte("users"))
	old = appendPart(old, []byte("email"))
	old = appendPart(old, []byte{0x00})
	old = appendPart(old, []byte(testCursorID))

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty"},
		{name: "invalid base64", token: "!"},
		{name: "truncated", token: base64.RawURLEncoding.EncodeToString([]byte{queryCursorVersion})},
		{name: "fabricated", token: base64.RawURLEncoding.EncodeToString(data(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0x00}, testCursorID, nil))},
		{name: "old version", token: sealQueryCursor(t, old)},
		{name: "unsupported version", token: token(queryCursorVersion+1, "users", "email", byte(cmpEq), []byte{0x00}, testCursorID, nil)},
		{name: "unknown operator", token: token(queryCursorVersion, "users", "email", 0xff, []byte{0x00}, testCursorID, nil)},
		{name: "invalid database", token: token(queryCursorVersion, "Bad", "email", byte(cmpEq), []byte{0x00}, testCursorID, nil)},
		{name: "invalid index", token: token(queryCursorVersion, "users", "Bad", byte(cmpEq), []byte{0x00}, testCursorID, nil)},
		{name: "invalid value", token: token(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0xff}, testCursorID, nil)},
		{name: "invalid id", token: token(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0x00}, "bad", nil)},
		{name: "trailing data", token: token(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0x00}, testCursorID, []byte{0})},
		{name: "oversized", token: strings.Repeat("a", base64.RawURLEncoding.EncodedLen(maxEncryptedQueryCursorSize)+1)},
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

func sealQueryCursor(t *testing.T, data []byte) string {
	t.Helper()

	box, err := getQueryCipher()
	if err != nil {
		t.Fatalf("getQueryCipher() error = %v", err)
	}
	nonce := make([]byte, box.NonceSize())
	sealed := box.Seal(append([]byte(nil), nonce...), nonce, data, nil)

	return base64.RawURLEncoding.EncodeToString(sealed)
}
