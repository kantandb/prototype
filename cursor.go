package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"math/big"
	"unicode/utf8"
)

const (
	queryCursorVersion = 1
	maxQueryCursorSize = 1 + 10 + 63 + 10 + maxIndexValue + 10 + 36
)

var errInvalidQueryCursor = errors.New("invalid query cursor")

type queryCursor struct {
	index string
	value []byte
	id    string
}

func encodeQueryCursor(cursor queryCursor) (string, error) {
	if err := validateName(cursor.index); err != nil || !validCursorValue(cursor.value) || validateID(cursor.id) != nil {
		return "", errInvalidQueryCursor
	}

	data := []byte{queryCursorVersion}
	data = appendPart(data, []byte(cursor.index))
	data = appendPart(data, cursor.value)
	data = appendPart(data, []byte(cursor.id))

	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeQueryCursor(token string) (queryCursor, error) {
	if token == "" || len(token) > base64.RawURLEncoding.EncodedLen(maxQueryCursorSize) {
		return queryCursor{}, errInvalidQueryCursor
	}

	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != token || len(data) == 0 || len(data) > maxQueryCursorSize || data[0] != queryCursorVersion {
		return queryCursor{}, errInvalidQueryCursor
	}

	index, rest, ok := readPart(data[1:])
	if !ok {
		return queryCursor{}, errInvalidQueryCursor
	}
	value, rest, ok := readPart(rest)
	if !ok {
		return queryCursor{}, errInvalidQueryCursor
	}
	id, rest, ok := readPart(rest)
	if !ok || len(rest) != 0 {
		return queryCursor{}, errInvalidQueryCursor
	}

	cursor := queryCursor{index: string(index), value: bytes.Clone(value), id: string(id)}
	if err := validateName(cursor.index); err != nil || !validCursorValue(cursor.value) || validateID(cursor.id) != nil {
		return queryCursor{}, errInvalidQueryCursor
	}

	return cursor, nil
}

func validCursorValue(value []byte) bool {
	if len(value) == 0 || len(value) > maxIndexValue {
		return false
	}

	switch value[0] {
	case 0x00, 0x01, 0x02:
		return len(value) == 1
	case 0x03:
		number, ok := new(big.Rat).SetString(string(value[1:]))

		return ok && number.RatString() == string(value[1:])
	case 0x04:
		return utf8.Valid(value[1:])
	default:
		return false
	}
}
