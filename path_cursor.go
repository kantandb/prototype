package main

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"unicode/utf8"
)

const (
	pathCursorVersion          = 1
	maxPathCursorSize          = 1 + 10 + len(queryMethod) + 1 + 1 + 10 + 63 + 10 + maxQueryPath + 10 + 63 + 1 + 10 + maxEncodedIndexValue + 10 + maxEncodedIndexValue + 10 + 36
	maxEncryptedPathCursorSize = maxPathCursorSize + 12 + 16
)

type pathDialect byte

const dialectJSONPath pathDialect = 1

type pathOrder byte

const (
	orderDocument pathOrder = iota + 1
	orderIndex
)

type pathCursor struct {
	method    string
	dialect   pathDialect
	order     pathOrder
	database  string
	path      string
	index     string
	op        cmpOp
	value     []byte
	lastValue []byte
	id        string
}

func encodePathCursor(box cipher.AEAD, cursor pathCursor) (string, error) {
	if err := validatePathCursor(cursor); err != nil {
		return "", errInvalidQueryCursor
	}

	data := []byte{pathCursorVersion}
	data = appendPart(data, []byte(cursor.method))
	data = append(data, byte(cursor.dialect), byte(cursor.order))
	data = appendPart(data, []byte(cursor.database))
	data = appendPart(data, []byte(cursor.path))
	data = appendPart(data, []byte(cursor.index))
	data = append(data, byte(cursor.op))
	data = appendPart(data, cursor.value)
	data = appendPart(data, cursor.lastValue)
	data = appendPart(data, []byte(cursor.id))

	nonce := make([]byte, box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating path cursor nonce: %w", err)
	}
	sealed := box.Seal(append([]byte(nil), nonce...), nonce, data, nil)

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decodePathCursor(box cipher.AEAD, token string) (pathCursor, error) {
	if token == "" || len(token) > base64.RawURLEncoding.EncodedLen(maxEncryptedPathCursorSize) {
		return pathCursor{}, errInvalidQueryCursor
	}

	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(sealed) != token || len(sealed) > maxEncryptedPathCursorSize {
		return pathCursor{}, errInvalidQueryCursor
	}
	if len(sealed) < box.NonceSize()+box.Overhead() {
		return pathCursor{}, errInvalidQueryCursor
	}

	nonce := sealed[:box.NonceSize()]
	data, err := box.Open(nil, nonce, sealed[box.NonceSize():], nil)
	if err != nil || len(data) == 0 || len(data) > maxPathCursorSize || data[0] != pathCursorVersion {
		return pathCursor{}, errInvalidQueryCursor
	}

	method, rest, ok := readPart(data[1:])
	if !ok || len(rest) < 2 {
		return pathCursor{}, errInvalidQueryCursor
	}
	dialect, order := pathDialect(rest[0]), pathOrder(rest[1])
	database, rest, ok := readPart(rest[2:])
	if !ok {
		return pathCursor{}, errInvalidQueryCursor
	}
	path, rest, ok := readPart(rest)
	if !ok {
		return pathCursor{}, errInvalidQueryCursor
	}
	index, rest, ok := readPart(rest)
	if !ok || len(rest) == 0 {
		return pathCursor{}, errInvalidQueryCursor
	}
	op := cmpOp(rest[0])
	value, rest, ok := readPart(rest[1:])
	if !ok {
		return pathCursor{}, errInvalidQueryCursor
	}
	lastValue, rest, ok := readPart(rest)
	if !ok {
		return pathCursor{}, errInvalidQueryCursor
	}
	id, rest, ok := readPart(rest)
	if !ok || len(rest) != 0 {
		return pathCursor{}, errInvalidQueryCursor
	}

	cursor := pathCursor{
		method:    string(method),
		dialect:   dialect,
		order:     order,
		database:  string(database),
		path:      string(path),
		index:     string(index),
		op:        op,
		value:     bytes.Clone(value),
		lastValue: bytes.Clone(lastValue),
		id:        string(id),
	}
	if err := validatePathCursor(cursor); err != nil {
		return pathCursor{}, errInvalidQueryCursor
	}

	return cursor, nil
}

func validatePathCursor(cursor pathCursor) error {
	if cursor.method != queryMethod || cursor.dialect != dialectJSONPath || cursor.order != orderDocument && cursor.order != orderIndex {
		return errInvalidQueryCursor
	}
	if validateName(cursor.database) != nil || len(cursor.path) == 0 || len(cursor.path) > maxQueryPath || !utf8.ValidString(cursor.path) {
		return errInvalidQueryCursor
	}
	if !cursor.op.valid() || !validIndexValue(cursor.value) || validateID(cursor.id) != nil {
		return errInvalidQueryCursor
	}
	if cursor.order == orderDocument {
		if cursor.index != "" || len(cursor.lastValue) != 0 {
			return errInvalidQueryCursor
		}

		return nil
	}
	if validateName(cursor.index) != nil {
		return errInvalidQueryCursor
	}
	if cursor.op == cmpEq && len(cursor.lastValue) != 0 {
		return errInvalidQueryCursor
	}
	if cursor.op != cmpEq && (!validIndexValue(cursor.lastValue) || cursor.lastValue[0] != cursor.value[0]) {
		return errInvalidQueryCursor
	}

	return nil
}

func matchPathCursor(cursor pathCursor, database string, plan pathPlan, query pathQuery, encoded []byte) error {
	order := orderDocument
	if plan.index != "" {
		order = orderIndex
	}
	if cursor.method != queryMethod || cursor.dialect != dialectJSONPath || cursor.order != order || cursor.database != database || cursor.path != plan.text || cursor.index != plan.index || cursor.op != query.op || !bytes.Equal(cursor.value, encoded) {
		return errInvalidQueryCursor
	}

	return nil
}
