package main

import (
	"bytes"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func TestStoreDatabases(t *testing.T) {
	t.Parallel()

	store := testStore(t)

	for _, name := range []string{"beta", "alpha"} {
		if err := store.createDB(name); err != nil {
			t.Fatalf("createDB(%q) error = %v", name, err)
		}
	}
	if err := store.createDB("alpha"); !errors.Is(err, errDBExists) {
		t.Fatalf("createDB() error = %v, want %v", err, errDBExists)
	}

	names, err := store.listDBs(100, "")
	if err != nil {
		t.Fatalf("listDBs() error = %v", err)
	}
	if want := []string{"alpha", "beta"}; !slices.Equal(names, want) {
		t.Errorf("listDBs() = %v, want %v", names, want)
	}

	names, err = store.listDBs(1, "alpha")
	if err != nil {
		t.Fatalf("listDBs() with cursor error = %v", err)
	}
	if want := []string{"beta"}; !slices.Equal(names, want) {
		t.Errorf("listDBs() with cursor = %v, want %v", names, want)
	}

	names, err = store.listDBs(1, "beta")
	if err != nil {
		t.Fatalf("listDBs() final page error = %v", err)
	}
	if len(names) != 0 {
		t.Errorf("listDBs() final page = %v, want empty", names)
	}

	if err := store.deleteDB("alpha"); err != nil {
		t.Fatalf("deleteDB() error = %v", err)
	}
	if err := store.deleteDB("alpha"); !errors.Is(err, errDBNotFound) {
		t.Fatalf("deleteDB() error = %v, want %v", err, errDBNotFound)
	}
}

func TestStoreDocuments(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("db"); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}

	body := []byte(`{"name":"first"}`)
	rev, err := store.createDoc("db", "01950000-0000-7000-8000-000000000001", body)
	if err != nil {
		t.Fatalf("createDoc() error = %v", err)
	}
	if rev == (revision{}) {
		t.Error("createDoc() revision is zero")
	}
	if _, err := store.createDoc("missing", "id", body); !errors.Is(err, errDBNotFound) {
		t.Fatalf("createDoc() error = %v, want %v", err, errDBNotFound)
	}
	if _, err := store.createDoc("db", "01950000-0000-7000-8000-000000000001", body); !errors.Is(err, errDocExists) {
		t.Fatalf("createDoc() error = %v, want %v", err, errDocExists)
	}

	doc, err := store.getDoc("db", "01950000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("getDoc() error = %v", err)
	}
	if !bytes.Equal(doc.json, body) {
		t.Errorf("getDoc() JSON = %s, want %s", doc.json, body)
	}
	if doc.revision != rev {
		t.Errorf("getDoc() revision = %x, want %x", doc.revision, rev)
	}

	replacement := []byte(`{"name":"second"}`)
	newRev, err := store.replaceDoc("db", "01950000-0000-7000-8000-000000000001", replacement, matchCond{})
	if err != nil {
		t.Fatalf("replaceDoc() error = %v", err)
	}
	if newRev == rev {
		t.Error("replaceDoc() did not change revision")
	}
	doc, err = store.getDoc("db", "01950000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("getDoc() after replace error = %v", err)
	}
	if !bytes.Equal(doc.json, replacement) {
		t.Errorf("getDoc() JSON = %s, want %s", doc.json, replacement)
	}

	if err := store.deleteDoc("db", "01950000-0000-7000-8000-000000000001", matchCond{}); err != nil {
		t.Fatalf("deleteDoc() error = %v", err)
	}
	if _, err := store.getDoc("db", "01950000-0000-7000-8000-000000000001"); !errors.Is(err, errDocNotFound) {
		t.Fatalf("getDoc() error = %v, want %v", err, errDocNotFound)
	}
	if err := store.deleteDoc("db", "01950000-0000-7000-8000-000000000001", matchCond{}); !errors.Is(err, errDocNotFound) {
		t.Fatalf("deleteDoc() error = %v, want %v", err, errDocNotFound)
	}
}

func TestStoreListsDocuments(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	for _, name := range []string{"db", "other", "empty"} {
		if err := store.createDB(name); err != nil {
			t.Fatalf("createDB(%q) error = %v", name, err)
		}
	}

	ids := []string{
		"01950000-0000-7000-8000-000000000003",
		"01950000-0000-7000-8000-000000000001",
		"01950000-0000-7000-8000-000000000002",
	}
	for _, id := range ids {
		if _, err := store.createDoc("db", id, []byte(`{"ok":true}`)); err != nil {
			t.Fatalf("createDoc(%q) error = %v", id, err)
		}
	}
	if _, err := store.createDoc("other", "01950000-0000-7000-8000-000000000000", []byte(`{}`)); err != nil {
		t.Fatalf("createDoc(other) error = %v", err)
	}

	got, more, err := store.listDocs("db", 2, "")
	if err != nil {
		t.Fatalf("listDocs() error = %v", err)
	}
	if !more {
		t.Error("listDocs() more = false, want true")
	}
	want := []string{ids[1], ids[2]}
	if !slices.Equal(got, want) {
		t.Errorf("listDocs() = %v, want %v", got, want)
	}

	got, more, err = store.listDocs("db", 2, ids[2])
	if err != nil {
		t.Fatalf("listDocs() with cursor error = %v", err)
	}
	if more {
		t.Error("listDocs() with cursor more = true, want false")
	}
	if want = []string{ids[0]}; !slices.Equal(got, want) {
		t.Errorf("listDocs() with cursor = %v, want %v", got, want)
	}

	got, more, err = store.listDocs("empty", 2, "")
	if err != nil {
		t.Fatalf("listDocs(empty) error = %v", err)
	}
	if more {
		t.Error("listDocs(empty) more = true, want false")
	}
	if len(got) != 0 {
		t.Errorf("listDocs(empty) = %v, want empty", got)
	}
	if _, _, err := store.listDocs("missing", 2, ""); !errors.Is(err, errDBNotFound) {
		t.Fatalf("listDocs(missing) error = %v, want %v", err, errDBNotFound)
	}
}

func TestDeleteDBDeletesOwnDocuments(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	for _, name := range []string{"a", "ab"} {
		if err := store.createDB(name); err != nil {
			t.Fatalf("createDB(%q) error = %v", name, err)
		}
		if _, err := store.createDoc(name, "id", []byte(`{"ok":true}`)); err != nil {
			t.Fatalf("createDoc(%q) error = %v", name, err)
		}
	}

	if err := store.deleteDB("a"); err != nil {
		t.Fatalf("deleteDB() error = %v", err)
	}
	if _, err := store.getDoc("a", "id"); !errors.Is(err, errDocNotFound) {
		t.Fatalf("getDoc(a) error = %v, want %v", err, errDocNotFound)
	}
	if _, err := store.getDoc("ab", "id"); err != nil {
		t.Fatalf("getDoc(ab) error = %v", err)
	}
}

func TestStorePersists(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if err := store.createDB("db"); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	rev, err := store.createDoc("db", "id", []byte(`{"saved":true}`))
	if err != nil {
		t.Fatalf("createDoc() error = %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("store.close() error = %v", err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("store.close() error = %v", err)
		}
	})

	doc, err := store.getDoc("db", "id")
	if err != nil {
		t.Fatalf("getDoc() error = %v", err)
	}
	if doc.revision != rev || !bytes.Equal(doc.json, []byte(`{"saved":true}`)) {
		t.Errorf("getDoc() = %+v, want persisted document", doc)
	}
}

func TestConcurrentCreateDB(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	const workers = 8

	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			errs <- store.createDB("same")
		})
	}
	wg.Wait()
	close(errs)

	var created, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, errDBExists):
			conflicts++
		default:
			t.Fatalf("createDB() unexpected error = %v", err)
		}
	}
	if created != 1 || conflicts != workers-1 {
		t.Errorf("created = %d, conflicts = %d", created, conflicts)
	}
}

func TestConcurrentCreateDoc(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("db"); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			_, err := store.createDoc("db", "id", []byte(`{"ok":true}`))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	var created, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, errDocExists):
			conflicts++
		default:
			t.Fatalf("createDoc() unexpected error = %v", err)
		}
	}
	if created != 1 || conflicts != workers-1 {
		t.Errorf("created = %d, conflicts = %d", created, conflicts)
	}
}

func TestConcurrentConditionalReplace(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("db"); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	rev, err := store.createDoc("db", "id", []byte(`{"value":0}`))
	if err != nil {
		t.Fatalf("createDoc() error = %v", err)
	}

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			_, err := store.replaceDoc("db", "id", []byte(`{"value":1}`), matchCond{set: true, revision: rev})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	var replaced, rejected int
	for err := range errs {
		switch {
		case err == nil:
			replaced++
		case errors.Is(err, errPreconditionFailed):
			rejected++
		default:
			t.Fatalf("replaceDoc() unexpected error = %v", err)
		}
	}
	if replaced != 1 || rejected != workers-1 {
		t.Errorf("replaced = %d, rejected = %d", replaced, rejected)
	}
}

func TestDistinctDocsDoNotBlock(t *testing.T) {
	t.Parallel()

	const (
		heldID  = "01950000-0000-7000-8000-000000000001"
		otherID = "01950000-0000-7000-8000-000000000002"
	)

	store := testStore(t)
	if err := store.createDB("db", indexDef{name: "name", path: "/name"}); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	if _, err := store.createDoc("db", heldID, []byte(`{"name":"old"}`)); err != nil {
		t.Fatalf("createDoc() error = %v", err)
	}
	if store.docLock("db", heldID) == store.docLock("db", otherID) {
		t.Fatal("test documents share a lock stripe")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	patched := make(chan error, 1)
	go func() {
		_, err := store.patchDoc("db", heldID, matchCond{}, func([]byte) ([]byte, error) {
			close(entered)
			<-release

			return []byte(`{"name":"new"}`), nil
		})
		patched <- err
	}()
	<-entered

	created := make(chan error, 1)
	go func() {
		_, err := store.createDoc("db", otherID, []byte(`{"name":"other"}`))
		created <- err
	}()

	select {
	case err := <-created:
		if err != nil {
			t.Fatalf("createDoc(other) error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("createDoc(other) blocked on distinct document")
	}

	close(release)
	if err := <-patched; err != nil {
		t.Fatalf("patchDoc() error = %v", err)
	}
	assertQuery(t, store, "db", "name", "new", []string{heldID})
	assertQuery(t, store, "db", "name", "other", []string{otherID})
}

func TestDeleteDBRacesWithWrites(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	for range 4 {
		if err := store.createDB("db", indexDef{name: "name", path: "/name"}); err != nil {
			t.Fatalf("createDB() error = %v", err)
		}
		for _, id := range []string{"replace", "patch", "delete"} {
			if _, err := store.createDoc("db", id, []byte(`{"name":"old"}`)); err != nil {
				t.Fatalf("createDoc(%q) error = %v", id, err)
			}
		}

		start := make(chan struct{})
		errs := make(chan error, 5)
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			_, err := store.replaceDoc("db", "replace", []byte(`{"name":"new"}`), matchCond{})
			errs <- err
		})
		wg.Go(func() {
			<-start
			_, err := store.patchDoc("db", "patch", matchCond{}, func([]byte) ([]byte, error) {
				return []byte(`{"name":"new"}`), nil
			})
			errs <- err
		})
		wg.Go(func() {
			<-start
			errs <- store.deleteDoc("db", "delete", matchCond{})
		})
		wg.Go(func() {
			<-start
			_, err := store.createDoc("db", "create", []byte(`{"name":"new"}`))
			errs <- err
		})
		wg.Go(func() {
			<-start
			errs <- store.deleteDB("db")
		})

		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil && !errors.Is(err, errDBNotFound) && !errors.Is(err, errDocNotFound) {
				t.Fatalf("concurrent mutation error = %v", err)
			}
		}

		exists, err := store.hasDB("db")
		if err != nil {
			t.Fatalf("hasDB() error = %v", err)
		}
		if exists {
			t.Fatal("database remains after deletion")
		}
		for _, prefix := range [][]byte{docsPrefix("db"), indexDefPrefix("db"), indexDataPrefix("db")} {
			iter, err := store.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
			if err != nil {
				t.Fatalf("NewIter() error = %v", err)
			}
			if iter.First() {
				t.Errorf("key remains after database deletion: %x", iter.Key())
			}
			if err := iter.Close(); err != nil {
				t.Fatalf("iterator close error = %v", err)
			}
		}
	}
}

func TestStoreRejectsCorruption(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.db.Set(dbKey("bad"), []byte{0xff}, pebble.Sync); err != nil {
		t.Fatalf("DB.Set() error = %v", err)
	}
	if _, err := store.listDBs(100, ""); err == nil {
		t.Fatal("listDBs() error = nil, want corruption error")
	}
	if _, err := store.createDoc("bad", "id", []byte(`{}`)); err == nil {
		t.Fatal("createDoc() error = nil, want corruption error")
	}

	if err := store.createDB("good"); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	if err := store.db.Set(docKey("good", "id"), []byte{0xff}, pebble.Sync); err != nil {
		t.Fatalf("DB.Set() error = %v", err)
	}
	if _, err := store.getDoc("good", "id"); err == nil {
		t.Fatal("getDoc() error = nil, want corruption error")
	}
	if _, _, err := store.listDocs("good", 100, ""); !errors.Is(err, errCorruptData) {
		t.Fatalf("listDocs() error = %v, want %v", err, errCorruptData)
	}
}

func TestDecodeDocRejectsCorruption(t *testing.T) {
	t.Parallel()

	tests := [][]byte{
		nil,
		{docRecordVersion},
		append([]byte{docRecordVersion + 1}, make([]byte, 16)...),
	}
	for _, value := range tests {
		if _, err := decodeDoc(value); err == nil {
			t.Errorf("decodeDoc(%x) error = nil, want error", value)
		}
	}
}

func testStore(t *testing.T) *store {
	t.Helper()

	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("store.close() error = %v", err)
		}
	})

	return store
}
