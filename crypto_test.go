package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble"
)

func TestStoreMetadata(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	store, err := openStore(path, testMasterKey)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}

	store, err = openStore(path, testMasterKey)
	if err != nil {
		t.Fatalf("openStore() after close error = %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close() after reopen error = %v", err)
	}

	wrong := bytes.Repeat([]byte{0xff}, keySize)
	if _, err := openStore(path, wrong); !errors.Is(err, errInvalidStoreKey) {
		t.Fatalf("openStore() wrong-key error = %v, want %v", err, errInvalidStoreKey)
	}
}

func TestStoreRejectsMissingMetadata(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	db, err := pebble.Open(path, storeOptions())
	if err != nil {
		t.Fatalf("pebble.Open() error = %v", err)
	}
	if err := db.Set(dbKey("legacy"), []byte("plaintext"), pebble.Sync); err != nil {
		t.Fatalf("DB.Set() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("DB.Close() error = %v", err)
	}

	if _, err := openStore(path, testMasterKey); !errors.Is(err, errInvalidStoreKey) {
		t.Fatalf("openStore() error = %v, want %v", err, errInvalidStoreKey)
	}
}

func TestStoreRejectsTamperedMetadata(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	store, err := openStore(path, testMasterKey)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	value := readValue(t, store.db, storeMetaKey)
	value[len(value)-1] ^= 1
	if err := store.db.Set(storeMetaKey, value, pebble.Sync); err != nil {
		t.Fatalf("DB.Set() error = %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}

	if _, err := openStore(path, testMasterKey); !errors.Is(err, errInvalidStoreKey) {
		t.Fatalf("openStore() error = %v, want %v", err, errInvalidStoreKey)
	}
}

func TestDatabaseKeyWrapping(t *testing.T) {
	t.Parallel()

	wrappingKey := bytes.Repeat([]byte{1}, keySize)
	databaseKey := bytes.Repeat([]byte{2}, keySize)
	key := dbKey("db")
	record, err := wrapDBKey(wrappingKey, key, databaseKey)
	if err != nil {
		t.Fatalf("wrapDBKey() error = %v", err)
	}
	got, err := unwrapDBKey(wrappingKey, key, record)
	if err != nil {
		t.Fatalf("unwrapDBKey() error = %v", err)
	}
	if !bytes.Equal(got, databaseKey) {
		t.Error("unwrapDBKey() returned wrong key")
	}

	if _, err := unwrapDBKey(wrappingKey, dbKey("other"), record); !errors.Is(err, errCorruptData) {
		t.Fatalf("unwrapDBKey() moved-record error = %v, want %v", err, errCorruptData)
	}
	record[0]++
	if _, err := unwrapDBKey(wrappingKey, key, record); !errors.Is(err, errCorruptData) {
		t.Fatalf("unwrapDBKey() version error = %v, want %v", err, errCorruptData)
	}
}

func readValue(t *testing.T, db *pebble.DB, key []byte) []byte {
	t.Helper()

	value, closer, err := db.Get(key)
	if err != nil {
		t.Fatalf("DB.Get() error = %v", err)
	}
	got := bytes.Clone(value)
	if err := closer.Close(); err != nil {
		t.Fatalf("closer.Close() error = %v", err)
	}

	return got
}
