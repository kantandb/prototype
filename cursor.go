package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

const (
	queryCursorVersion          = 3
	maxQueryCursorSize          = 1 + 10 + 63 + 10 + 63 + 1 + 10 + maxEncodedIndexValue + 10 + maxEncodedIndexValue + 10 + 36
	maxEncryptedQueryCursorSize = maxQueryCursorSize + 12 + 16
)

var (
	errInvalidQueryCursor = errors.New("invalid query cursor")
	queryCipherOnce       sync.Once
	queryCipher           cipher.AEAD
	queryCipherErr        error
)

type queryCursor struct {
	database  string
	index     string
	op        cmpOp
	value     []byte
	lastValue []byte
	id        string
}

func encodeQueryCursor(cursor queryCursor) (string, error) {
	if err := validateQueryCursor(cursor); err != nil {
		return "", errInvalidQueryCursor
	}

	data := []byte{queryCursorVersion}
	data = appendPart(data, []byte(cursor.database))
	data = appendPart(data, []byte(cursor.index))
	data = append(data, byte(cursor.op))
	data = appendPart(data, cursor.value)
	data = appendPart(data, cursor.lastValue)
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
	if !ok || len(rest) == 0 {
		return queryCursor{}, errInvalidQueryCursor
	}
	op := cmpOp(rest[0])
	rest = rest[1:]
	value, rest, ok := readPart(rest)
	if !ok {
		return queryCursor{}, errInvalidQueryCursor
	}
	lastValue, rest, ok := readPart(rest)
	if !ok {
		return queryCursor{}, errInvalidQueryCursor
	}
	id, rest, ok := readPart(rest)
	if !ok || len(rest) != 0 {
		return queryCursor{}, errInvalidQueryCursor
	}

	cursor := queryCursor{
		database:  string(database),
		index:     string(index),
		op:        op,
		value:     bytes.Clone(value),
		lastValue: bytes.Clone(lastValue),
		id:        string(id),
	}
	if err := validateQueryCursor(cursor); err != nil {
		return queryCursor{}, err
	}

	return cursor, nil
}

func validateQueryCursor(cursor queryCursor) error {
	if err := validateName(cursor.database); err != nil || validateName(cursor.index) != nil || !cursor.op.valid() || !validIndexValue(cursor.value) || validateID(cursor.id) != nil {
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
	if len(value) == 0 || len(value) > maxEncodedIndexValue {
		return false
	}

	switch value[0] {
	case 0x00, 0x01, 0x02:
		return len(value) == 1
	case 0x03:
		return validSortableNumber(value[1:])
	case 0x04:
		return validSortableString(value[1:])
	default:
		return false
	}
}

func validSortableNumber(value []byte) bool {
	if len(value) == 1 {
		return value[0] == 0x01
	}
	if len(value) < 11 || value[0] != 0x00 && value[0] != 0x02 {
		return false
	}

	negative := value[0] == 0x00
	orderedExponent := binary.BigEndian.Uint64(value[1:9])
	if negative {
		orderedExponent = ^orderedExponent
	}
	exponent := int64(orderedExponent ^ (uint64(1) << 63))
	if exponent < -maxIndexValue || exponent > maxIndexValue || len(value)-10 > maxIndexValue {
		return false
	}

	terminator, zero := byte(0x00), byte(0x01)
	if negative {
		terminator, zero = 0xff, 0xfe
	}
	if value[len(value)-1] != terminator || value[9] == zero || value[len(value)-2] == zero {
		return false
	}
	for _, digit := range value[9 : len(value)-1] {
		if !negative && (digit < 0x01 || digit > 0x0a) || negative && (digit < 0xf5 || digit > 0xfe) {
			return false
		}
	}

	return true
}

func validSortableString(value []byte) bool {
	decoded := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != 0x00 {
			decoded = append(decoded, value[i])
			continue
		}
		if i+1 >= len(value) {
			return false
		}
		switch value[i+1] {
		case 0x00:
			return i+2 == len(value) && len(decoded)+1 <= maxIndexValue && utf8.Valid(decoded)
		case 0xff:
			decoded = append(decoded, 0x00)
			i++
		default:
			return false
		}
	}

	return false
}
