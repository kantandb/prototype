package main

import (
	"bytes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

const testCursorID = "01950000-0000-7000-8000-000000000001"

func TestQueryCursorRoundTrip(t *testing.T) {
	t.Parallel()

	box := testQueryCipher(t)
	value, err := encodeIndexValue("alice@example.com")
	if err != nil {
		t.Fatalf("encodeIndexValue() error = %v", err)
	}
	want := queryCursor{database: "users", index: "email", op: cmpGE, value: value, lastValue: value, id: testCursorID}

	token, err := encodeQueryCursor(box, want)
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

	got, err := decodeQueryCursor(box, token)
	if err != nil {
		t.Fatalf("decodeQueryCursor() error = %v", err)
	}
	if got.database != want.database || got.index != want.index || got.op != want.op || !bytes.Equal(got.value, want.value) || !bytes.Equal(got.lastValue, want.lastValue) || got.id != want.id {
		t.Errorf("decodeQueryCursor() = %+v, want %+v", got, want)
	}
}

func TestQueryCursorSurvivesReopen(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if err := store.createDB("users"); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	box := storeQueryCipher(t, store, "users")
	token, err := encodeQueryCursor(box, queryCursor{database: "users", index: "email", op: cmpEq, value: []byte{0x00}, id: testCursorID})
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("store.close() error = %v", err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatalf("openStore() after close error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("store.close() error = %v", err)
		}
	})
	box = storeQueryCipher(t, store, "users")
	if _, err := decodeQueryCursor(box, token); err != nil {
		t.Fatalf("decodeQueryCursor() after reopen error = %v", err)
	}
}

func TestQueryCursorDatabaseKeys(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	for _, name := range []string{"users", "others"} {
		if err := store.createDB(name); err != nil {
			t.Fatalf("createDB(%q) error = %v", name, err)
		}
	}

	box := storeQueryCipher(t, store, "users")
	token, err := encodeQueryCursor(box, queryCursor{database: "users", index: "email", op: cmpEq, value: []byte{0x00}, id: testCursorID})
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}
	other := storeQueryCipher(t, store, "others")
	if _, err := decodeQueryCursor(other, token); !errors.Is(err, errInvalidQueryCursor) {
		t.Errorf("decodeQueryCursor(other) error = %v, want %v", err, errInvalidQueryCursor)
	}

	if err := store.deleteDB("users"); err != nil {
		t.Fatalf("deleteDB() error = %v", err)
	}
	if err := store.createDB("users"); err != nil {
		t.Fatalf("createDB(recreated) error = %v", err)
	}
	recreated := storeQueryCipher(t, store, "users")
	if _, err := decodeQueryCursor(recreated, token); !errors.Is(err, errInvalidQueryCursor) {
		t.Errorf("decodeQueryCursor(recreated) error = %v, want %v", err, errInvalidQueryCursor)
	}
}

func TestQueryCursorOperatorMatch(t *testing.T) {
	t.Parallel()

	box := testQueryCipher(t)
	value, err := encodeIndexValue("alice@example.com")
	if err != nil {
		t.Fatalf("encodeIndexValue() error = %v", err)
	}
	token, err := encodeQueryCursor(box, queryCursor{database: "users", index: "email", op: cmpLT, value: value, lastValue: value, id: testCursorID})
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}

	query := docQuery{index: "email", op: cmpGE, value: "alice@example.com", cursor: token}
	if _, _, _, err := queryStart(box, "users", query); !errors.Is(err, errInvalidQueryCursor) {
		t.Errorf("queryStart() error = %v, want %v", err, errInvalidQueryCursor)
	}
}

func TestQueryCursorTampering(t *testing.T) {
	t.Parallel()

	box := testQueryCipher(t)
	token, err := encodeQueryCursor(box, queryCursor{database: "users", index: "email", op: cmpEq, value: []byte{0x00}, id: testCursorID})
	if err != nil {
		t.Fatalf("encodeQueryCursor() error = %v", err)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	sealed[len(sealed)-1] ^= 1
	token = base64.RawURLEncoding.EncodeToString(sealed)

	if _, err := decodeQueryCursor(box, token); !errors.Is(err, errInvalidQueryCursor) {
		t.Errorf("decodeQueryCursor() error = %v, want %v", err, errInvalidQueryCursor)
	}
}

func TestSortableNumberValidation(t *testing.T) {
	t.Parallel()

	value := valueForCursor(t, 1)
	binary.BigEndian.PutUint64(value[2:10], uint64(maxIndexValue+1)^(uint64(1)<<63))
	if validIndexValue(value) {
		t.Error("validIndexValue() accepted an unreachable exponent")
	}
}

func TestQueryCursorValidation(t *testing.T) {
	t.Parallel()

	box := testQueryCipher(t)
	data := func(version byte, database, index string, op byte, value, lastValue []byte, id string, tail []byte) []byte {
		encoded := []byte{version}
		encoded = appendPart(encoded, []byte(database))
		encoded = appendPart(encoded, []byte(index))
		encoded = append(encoded, op)
		encoded = appendPart(encoded, value)
		encoded = appendPart(encoded, lastValue)
		encoded = appendPart(encoded, []byte(id))

		return append(encoded, tail...)
	}
	token := func(version byte, database, index string, op byte, value, lastValue []byte, id string, tail []byte) string {
		return sealQueryCursor(t, box, data(version, database, index, op, value, lastValue, id, tail))
	}

	old := []byte{queryCursorVersion - 1}
	old = appendPart(old, []byte("users"))
	old = appendPart(old, []byte("email"))
	old = append(old, byte(cmpEq))
	old = appendPart(old, []byte{0x00})
	old = appendPart(old, []byte(testCursorID))

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty"},
		{name: "invalid base64", token: "!"},
		{name: "truncated", token: base64.RawURLEncoding.EncodeToString([]byte{queryCursorVersion})},
		{name: "fabricated", token: base64.RawURLEncoding.EncodeToString(data(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0x00}, nil, testCursorID, nil))},
		{name: "old version", token: sealQueryCursor(t, box, old)},
		{name: "unsupported version", token: token(queryCursorVersion+1, "users", "email", byte(cmpEq), []byte{0x00}, nil, testCursorID, nil)},
		{name: "unknown operator", token: token(queryCursorVersion, "users", "email", 0xff, []byte{0x00}, nil, testCursorID, nil)},
		{name: "invalid database", token: token(queryCursorVersion, "Bad", "email", byte(cmpEq), []byte{0x00}, nil, testCursorID, nil)},
		{name: "invalid index", token: token(queryCursorVersion, "users", "Bad", byte(cmpEq), []byte{0x00}, nil, testCursorID, nil)},
		{name: "invalid value", token: token(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0xff}, nil, testCursorID, nil)},
		{name: "equality last value", token: token(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0x00}, []byte{0x00}, testCursorID, nil)},
		{name: "missing range last value", token: token(queryCursorVersion, "users", "email", byte(cmpGE), valueForCursor(t, "a"), nil, testCursorID, nil)},
		{name: "range last value type", token: token(queryCursorVersion, "users", "email", byte(cmpGE), valueForCursor(t, "a"), valueForCursor(t, 1), testCursorID, nil)},
		{name: "invalid id", token: token(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0x00}, nil, "bad", nil)},
		{name: "trailing data", token: token(queryCursorVersion, "users", "email", byte(cmpEq), []byte{0x00}, nil, testCursorID, []byte{0})},
		{name: "oversized", token: strings.Repeat("a", base64.RawURLEncoding.EncodedLen(maxEncryptedQueryCursorSize)+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := decodeQueryCursor(box, tt.token); !errors.Is(err, errInvalidQueryCursor) {
				t.Errorf("decodeQueryCursor() error = %v, want %v", err, errInvalidQueryCursor)
			}
		})
	}
}

func valueForCursor(t *testing.T, value any) []byte {
	t.Helper()

	encoded, err := encodeIndexValue(value)
	if err != nil {
		t.Fatalf("encodeIndexValue() error = %v", err)
	}

	return encoded
}

func storeQueryCipher(t *testing.T, store *store, database string) cipher.AEAD {
	t.Helper()

	key, err := store.cursorKey(database)
	if err != nil {
		t.Fatalf("cursorKey() error = %v", err)
	}
	box, err := newQueryCipher(key)
	if err != nil {
		t.Fatalf("newQueryCipher() error = %v", err)
	}

	return box
}

func testQueryCipher(t *testing.T) cipher.AEAD {
	t.Helper()

	box, err := newQueryCipher(make([]byte, cursorKeySize))
	if err != nil {
		t.Fatalf("newQueryCipher() error = %v", err)
	}

	return box
}

func sealQueryCursor(t *testing.T, box cipher.AEAD, data []byte) string {
	t.Helper()

	nonce := make([]byte, box.NonceSize())
	sealed := box.Seal(append([]byte(nil), nonce...), nonce, data, nil)

	return base64.RawURLEncoding.EncodeToString(sealed)
}
