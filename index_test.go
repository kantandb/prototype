package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

func TestStoreOptionsEnableBloom(t *testing.T) {
	t.Parallel()

	options := storeOptions().EnsureDefaults()
	if got := options.Levels[0].FilterPolicy.Name(); got != "rocksdb.BuiltinBloomFilter" {
		t.Errorf("FilterPolicy.Name() = %q, want Bloom filter", got)
	}
}

func TestValidateIndexes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		defs []indexDef
	}{
		{name: "invalid name", defs: []indexDef{{name: "Bad", path: "/name"}}},
		{name: "empty path", defs: []indexDef{{name: "name", path: ""}}},
		{name: "relative path", defs: []indexDef{{name: "name", path: "name"}}},
		{name: "invalid escape", defs: []indexDef{{name: "name", path: "/a~2b"}}},
		{name: "duplicate", defs: []indexDef{{name: "name", path: "/a"}, {name: "name", path: "/b"}}},
		{name: "long path", defs: []indexDef{{name: "name", path: "/" + strings.Repeat("a", maxIndexPath)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := validateIndexes(tt.defs); !errors.Is(err, errInvalidIndex) {
				t.Errorf("validateIndexes() error = %v, want %v", err, errInvalidIndex)
			}
		})
	}

	tooMany := make([]indexDef, maxIndexes+1)
	if err := validateIndexes(tooMany); !errors.Is(err, errInvalidIndex) {
		t.Errorf("validateIndexes(too many) error = %v, want %v", err, errInvalidIndex)
	}
}

func TestIndexValueEncoding(t *testing.T) {
	t.Parallel()

	numbers := []json.Number{"1", "1.0", "1e0"}
	first, err := encodeIndexValue(numbers[0])
	if err != nil {
		t.Fatalf("encodeIndexValue() error = %v", err)
	}
	for _, number := range numbers[1:] {
		got, err := encodeIndexValue(number)
		if err != nil {
			t.Fatalf("encodeIndexValue(%q) error = %v", number, err)
		}
		if !bytes.Equal(got, first) {
			t.Errorf("encodeIndexValue(%q) = %x, want %x", number, got, first)
		}
	}
	integer, err := encodeIndexValue(1)
	if err != nil {
		t.Fatalf("encodeIndexValue(int) error = %v", err)
	}
	if !bytes.Equal(integer, first) {
		t.Errorf("encodeIndexValue(int) = %x, want %x", integer, first)
	}

	truth, err := encodeIndexValue(true)
	if err != nil {
		t.Fatalf("encodeIndexValue(true) error = %v", err)
	}
	text, err := encodeIndexValue("true")
	if err != nil {
		t.Fatalf("encodeIndexValue(string) error = %v", err)
	}
	if bytes.Equal(truth, text) {
		t.Error("boolean and string encodings collide")
	}

	for _, number := range []json.Number{"", "01", "+1", ".1", "1/2"} {
		if _, err := encodeIndexValue(number); !errors.Is(err, errInvalidIndexValue) {
			t.Errorf("encodeIndexValue(%q) error = %v, want %v", number, err, errInvalidIndexValue)
		}
	}
	if _, err := encodeIndexValue([]any{1}); !errors.Is(err, errInvalidIndexValue) {
		t.Errorf("encodeIndexValue(array) error = %v, want %v", err, errInvalidIndexValue)
	}
	if _, err := encodeIndexValue(strings.Repeat("x", maxIndexValue)); !errors.Is(err, errInvalidIndexValue) {
		t.Errorf("encodeIndexValue(large) error = %v, want %v", err, errInvalidIndexValue)
	}
}

func TestIndexKeyParts(t *testing.T) {
	t.Parallel()

	parts := [][]byte{[]byte("db"), []byte("a\x00/b"), []byte("value")}
	key := []byte{0x04}
	for _, part := range parts {
		key = appendPart(key, part)
	}

	rest := key[1:]
	for _, want := range parts {
		got, next, ok := readPart(rest)
		if !ok || !bytes.Equal(got, want) {
			t.Fatalf("readPart() = %q, %t, want %q, true", got, ok, want)
		}
		rest = next
	}
	if len(rest) != 0 {
		t.Errorf("readPart() remainder = %x, want empty", rest)
	}
}

func TestStoreIndexesDocuments(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	defs := []indexDef{
		{name: "email", path: "/email"},
		{name: "active", path: "/active"},
		{name: "empty", path: "/empty"},
		{name: "escaped", path: "/profile/a~1b~0c"},
		{name: "item", path: "/items/0/sku"},
		{name: "object", path: "/profile"},
	}
	if err := store.createDB("users", defs...); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	if err := store.createDB("other", defs...); err != nil {
		t.Fatalf("createDB(other) error = %v", err)
	}

	documents := map[string]string{
		"c": `{"email":"other@example.com","active":false}`,
		"a": `{"email":"alice@example.com","active":true,"empty":null,"profile":{"a/b~c":"yes"},"items":[{"sku":"first"}]}`,
		"b": `{"email":"alice@example.com","active":true,"profile":{"a/b~c":"yes"}}`,
	}
	for id, document := range documents {
		if _, err := store.createDoc("users", id, []byte(document)); err != nil {
			t.Fatalf("createDoc(%q) error = %v", id, err)
		}
	}
	if _, err := store.createDoc("other", "z", []byte(`{"email":"alice@example.com"}`)); err != nil {
		t.Fatalf("createDoc(other) error = %v", err)
	}

	assertQuery(t, store, "users", "email", "alice@example.com", []string{"a", "b"})
	assertQuery(t, store, "users", "active", true, []string{"a", "b"})
	assertQuery(t, store, "users", "empty", nil, []string{"a"})
	assertQuery(t, store, "users", "escaped", "yes", []string{"a", "b"})
	assertQuery(t, store, "users", "item", "first", []string{"a"})
	assertQuery(t, store, "users", "object", "ignored", nil)

	ids, more, err := store.queryDocs("users", "email", "alice@example.com", 1, "")
	if err != nil {
		t.Fatalf("queryDocs() error = %v", err)
	}
	if !more {
		t.Error("queryDocs() more = false, want true")
	}
	if want := []string{"a"}; !slices.Equal(ids, want) {
		t.Errorf("queryDocs() = %v, want %v", ids, want)
	}

	ids, more, err = store.queryDocs("users", "email", "alice@example.com", 1, "a")
	if err != nil {
		t.Fatalf("queryDocs(cursor) error = %v", err)
	}
	if more {
		t.Error("queryDocs(cursor) more = true, want false")
	}
	if want := []string{"b"}; !slices.Equal(ids, want) {
		t.Errorf("queryDocs(cursor) = %v, want %v", ids, want)
	}

	if _, _, err := store.queryDocs("users", "missing", "value", 10, ""); !errors.Is(err, errIndexNotFound) {
		t.Errorf("queryDocs(missing index) error = %v, want %v", err, errIndexNotFound)
	}
	if _, _, err := store.queryDocs("missing", "email", "value", 10, ""); !errors.Is(err, errDBNotFound) {
		t.Errorf("queryDocs(missing database) error = %v, want %v", err, errDBNotFound)
	}
}

func TestStoreMaintainsIndexes(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("db", indexDef{name: "name", path: "/name"}); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}

	rev, err := store.createDoc("db", "id", []byte(`{"name":"first"}`))
	if err != nil {
		t.Fatalf("createDoc() error = %v", err)
	}
	if _, err := store.replaceDoc("db", "id", []byte(`{"name":"blocked"}`), matchCond{set: true, revision: revision{1}}); !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("replaceDoc(stale) error = %v, want %v", err, errPreconditionFailed)
	}
	assertQuery(t, store, "db", "name", "first", []string{"id"})

	newRev, err := store.replaceDoc("db", "id", []byte(`{"name":"second"}`), matchCond{set: true, revision: rev})
	if err != nil {
		t.Fatalf("replaceDoc() error = %v", err)
	}
	assertQuery(t, store, "db", "name", "first", nil)
	assertQuery(t, store, "db", "name", "second", []string{"id"})

	doc, err := store.patchDoc("db", "id", matchCond{set: true, revision: newRev}, func([]byte) ([]byte, error) {
		return []byte(`{"name":"third"}`), nil
	})
	if err != nil {
		t.Fatalf("patchDoc() error = %v", err)
	}
	assertQuery(t, store, "db", "name", "second", nil)
	assertQuery(t, store, "db", "name", "third", []string{"id"})

	if err := store.deleteDoc("db", "id", matchCond{set: true, revision: doc.revision}); err != nil {
		t.Fatalf("deleteDoc() error = %v", err)
	}
	assertQuery(t, store, "db", "name", "third", nil)
}

func TestStorePersistsIndexes(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if err := store.createDB("db", indexDef{name: "number", path: "/number"}); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	if _, err := store.createDoc("db", "id", []byte(`{"number":1.0}`)); err != nil {
		t.Fatalf("createDoc() error = %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("close() error = %v", err)
		}
	})

	defs, err := store.indexes("db")
	if err != nil {
		t.Fatalf("indexes() error = %v", err)
	}
	if len(defs) != 1 || defs[0].name != "number" || defs[0].path != "/number" {
		t.Errorf("indexes() = %+v, want number definition", defs)
	}
	assertQuery(t, store, "db", "number", json.Number("1e0"), []string{"id"})
}

func TestDeleteDBDeletesIndexes(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("db", indexDef{name: "name", path: "/name"}); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	if _, err := store.createDoc("db", "id", []byte(`{"name":"value"}`)); err != nil {
		t.Fatalf("createDoc() error = %v", err)
	}
	if err := store.deleteDB("db"); err != nil {
		t.Fatalf("deleteDB() error = %v", err)
	}

	for _, prefix := range [][]byte{indexDefPrefix("db"), indexDataPrefix("db")} {
		iter, err := store.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
		if err != nil {
			t.Fatalf("NewIter() error = %v", err)
		}
		if iter.First() {
			t.Errorf("index key remains after database deletion: %x", iter.Key())
		}
		if err := iter.Close(); err != nil {
			t.Fatalf("iterator close error = %v", err)
		}
	}
}

func TestStoreRejectsCorruptIndex(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("db", indexDef{name: "name", path: "/name"}); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	if err := store.db.Set(indexDefKey("db", "bad"), []byte{0xff}, pebble.Sync); err != nil {
		t.Fatalf("Set(definition) error = %v", err)
	}
	if _, err := store.indexes("db"); !errors.Is(err, errCorruptData) {
		t.Errorf("indexes() error = %v, want %v", err, errCorruptData)
	}

	if err := store.db.Delete(indexDefKey("db", "bad"), pebble.Sync); err != nil {
		t.Fatalf("Delete(definition) error = %v", err)
	}
	value, err := encodeIndexValue("value")
	if err != nil {
		t.Fatalf("encodeIndexValue() error = %v", err)
	}
	if err := store.db.Set(indexKey("db", "name", value, "id"), []byte{1}, pebble.Sync); err != nil {
		t.Fatalf("Set(entry) error = %v", err)
	}
	if _, _, err := store.queryDocs("db", "name", "value", 10, ""); !errors.Is(err, errCorruptData) {
		t.Errorf("queryDocs() error = %v, want %v", err, errCorruptData)
	}
}

func assertQuery(t *testing.T, store *store, database, index string, value any, want []string) {
	t.Helper()

	got, more, err := store.queryDocs(database, index, value, 100, "")
	if err != nil {
		t.Fatalf("queryDocs(%q, %q) error = %v", database, index, err)
	}
	if more {
		t.Errorf("queryDocs(%q, %q) more = true, want false", database, index)
	}
	if !slices.Equal(got, want) {
		t.Errorf("queryDocs(%q, %q) = %v, want %v", database, index, got, want)
	}
}
