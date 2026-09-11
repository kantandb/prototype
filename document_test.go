package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestDocumentCodec(t *testing.T) {
	t.Parallel()

	databaseKey := bytes.Repeat([]byte{3}, keySize)
	pebbleKey := docKey("db", "id")
	bodies := [][]byte{
		{},
		[]byte(`{}`),
		[]byte(`{"name":"small"}`),
		bytes.Repeat([]byte(`{"value":"compressible"}`), 4096),
	}

	for _, body := range bodies {
		rev, err := makeRevision(nil)
		if err != nil {
			t.Fatalf("makeRevision() error = %v", err)
		}
		record, err := sealDoc(pebbleKey, databaseKey, "id", rev, body)
		if err != nil {
			t.Fatalf("sealDoc(%d bytes) error = %v", len(body), err)
		}
		doc, err := openDoc(pebbleKey, databaseKey, "id", record)
		if err != nil {
			t.Fatalf("openDoc(%d bytes) error = %v", len(body), err)
		}
		if doc.revision != rev || !bytes.Equal(doc.json, body) {
			t.Errorf("openDoc(%d bytes) returned wrong document", len(body))
		}
	}
}

func TestDocumentCodecUsesFreshCiphertext(t *testing.T) {
	t.Parallel()

	databaseKey := bytes.Repeat([]byte{3}, keySize)
	key := docKey("db", "id")
	var rev revision
	first, err := sealDoc(key, databaseKey, "id", rev, []byte(`{"same":true}`))
	if err != nil {
		t.Fatalf("sealDoc() error = %v", err)
	}
	second, err := sealDoc(key, databaseKey, "id", rev, []byte(`{"same":true}`))
	if err != nil {
		t.Fatalf("sealDoc() second error = %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("sealDoc() reused ciphertext")
	}
	nonceOffset := 2 + len(rev) + 8
	if bytes.Equal(first[nonceOffset:docRecordHeaderLen], second[nonceOffset:docRecordHeaderLen]) {
		t.Fatal("sealDoc() reused nonce")
	}
}

func TestDocumentCodecRejectsCorruption(t *testing.T) {
	t.Parallel()

	databaseKey := bytes.Repeat([]byte{3}, keySize)
	key := docKey("db", "id")
	record, err := sealDoc(key, databaseKey, "id", revision{}, []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("sealDoc() error = %v", err)
	}

	for i := range record {
		corrupt := bytes.Clone(record)
		corrupt[i] ^= 1
		if _, err := openDoc(key, databaseKey, "id", corrupt); !errors.Is(err, errCorruptData) {
			t.Fatalf("openDoc() tamper at %d error = %v, want %v", i, err, errCorruptData)
		}
	}
	if _, err := openDoc(docKey("db", "other"), databaseKey, "other", record); !errors.Is(err, errCorruptData) {
		t.Fatalf("openDoc() moved-record error = %v, want %v", err, errCorruptData)
	}
	for _, short := range [][]byte{nil, record[:docRecordHeaderLen], record[:len(record)-1]} {
		if _, err := openDoc(key, databaseKey, "id", short); !errors.Is(err, errCorruptData) {
			t.Fatalf("openDoc() truncated error = %v, want %v", err, errCorruptData)
		}
	}
}

func TestDocumentCodecIncompressible(t *testing.T) {
	t.Parallel()

	body := make([]byte, 1<<20)
	if _, err := rand.Read(body); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	databaseKey := bytes.Repeat([]byte{3}, keySize)
	key := docKey("db", "id")
	record, err := sealDoc(key, databaseKey, "id", revision{}, body)
	if err != nil {
		t.Fatalf("sealDoc() error = %v", err)
	}
	doc, err := openDoc(key, databaseKey, "id", record)
	if err != nil {
		t.Fatalf("openDoc() error = %v", err)
	}
	if !bytes.Equal(doc.json, body) {
		t.Error("openDoc() returned wrong bytes")
	}
}
