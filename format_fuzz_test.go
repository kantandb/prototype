package main

import (
	"bytes"
	"testing"
)

func FuzzParseDocRecord(f *testing.F) {
	f.Add([]byte(nil))
	f.Add(make([]byte, docRecordHeaderLen+gcmTagLen))

	f.Fuzz(func(t *testing.T, value []byte) {
		_, _, _, _ = parseDocRecord(value)
	})
}

func FuzzOpenStoreMeta(f *testing.F) {
	f.Add([]byte(nil))
	f.Add(make([]byte, storeMetaHeaderLen+len(storeVerifier)+gcmTagLen))

	f.Fuzz(func(t *testing.T, value []byte) {
		key, _ := openStoreMeta(value, testMasterKey)
		clear(key)
	})
}

func FuzzUnwrapDBKey(f *testing.F) {
	f.Add([]byte(nil))
	f.Add(make([]byte, dbRecordHeaderLen+keySize+gcmTagLen))
	wrappingKey := bytes.Repeat([]byte{1}, keySize)

	f.Fuzz(func(t *testing.T, value []byte) {
		key, _ := unwrapDBKey(wrappingKey, dbKey("db"), value)
		clear(key)
	})
}
