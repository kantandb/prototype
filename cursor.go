package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"unicode/utf8"
)

const (
	queryCursorVersion          = 1
	maxQueryCursorSize          = 1 + 10 + 63 + 10 + 63 + 10 + maxIndexValue + 10 + 36
	maxEncryptedQueryCursorSize = maxQueryCursorSize + 12 + 16
)

var (
	errInvalidQueryCursor = errors.New("invalid query cursor")
	queryCipherOnce       sync.Once
	queryCipher           cipher.AEAD
	queryCipherErr        error
)

type queryCursor struct {
	database string
	index    string
	value    []byte
	id       string
}

func encodeQueryCursor(cursor queryCursor) (string, error) {
	if err := validateName(cursor.database); err != nil || validateName(cursor.index) != nil || !validIndexValue(cursor.value) || validateID(cursor.id) != nil {
		return "", errInvalidQueryCursor
	}

	data := []byte{queryCursorVersion}
	data = appendPart(data, []byte(cursor.database))
	data = appendPart(data, []byte(cursor.index))
	data = appendPart(data, cursor.value)
	data = appendPart(data, []byte(cursor.id))

	box, err := getQueryCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating cursor nonce: %w", err)
	}
	sealed := box.Seal(append([]byte(nil), nonce...), nonce, data, nil)

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decodeQueryCursor(token string) (queryCursor, error) {
	if token == "" || len(token) > base64.RawURLEncoding.EncodedLen(maxEncryptedQueryCursorSize) {
		return queryCursor{}, errInvalidQueryCursor
	}

	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(sealed) != token || len(sealed) > maxEncryptedQueryCursorSize {
		return queryCursor{}, errInvalidQueryCursor
	}

	box, err := getQueryCipher()
	if err != nil {
		return queryCursor{}, err
	}
	if len(sealed) < box.NonceSize()+box.Overhead() {
		return queryCursor{}, errInvalidQueryCursor
	}
	nonce := sealed[:box.NonceSize()]
	data, err := box.Open(nil, nonce, sealed[box.NonceSize():], nil)
	if err != nil || len(data) == 0 || len(data) > maxQueryCursorSize || data[0] != queryCursorVersion {
		return queryCursor{}, errInvalidQueryCursor
	}

	database, rest, ok := readPart(data[1:])
	if !ok {
		return queryCursor{}, errInvalidQueryCursor
	}
	index, rest, ok := readPart(rest)
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

	cursor := queryCursor{database: string(database), index: string(index), value: bytes.Clone(value), id: string(id)}
	if err := validateName(cursor.database); err != nil || validateName(cursor.index) != nil || !validIndexValue(cursor.value) || validateID(cursor.id) != nil {
		return queryCursor{}, errInvalidQueryCursor
	}

	return cursor, nil
}

func getQueryCipher() (cipher.AEAD, error) {
	queryCipherOnce.Do(func() {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			queryCipherErr = fmt.Errorf("generating cursor key: %w", err)

			return
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			queryCipherErr = fmt.Errorf("creating cursor cipher: %w", err)

			return
		}
		queryCipher, queryCipherErr = cipher.NewGCM(block)
	})

	return queryCipher, queryCipherErr
}

func validIndexValue(value []byte) bool {
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
