package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

var (
	errDBExists           = errors.New("database exists")
	errDBNotFound         = errors.New("database not found")
	errDocExists          = errors.New("document exists")
	errDocNotFound        = errors.New("document not found")
	errPreconditionFailed = errors.New("precondition failed")
	errCorruptData        = errors.New("corrupt stored data")
	errStoreUnavailable   = errors.New("storage unavailable")
)

var (
	dbPrefix      = []byte{0x01}
	docPrefix     = []byte{0x02}
	idxDefPrefix  = []byte{0x03}
	idxDataPrefix = []byte{0x04}
)

const recordVersion byte = 1

const (
	dbStripeCount  = 64
	docStripeCount = 256
	fnvOffset      = 14695981039346656037
	fnvPrime       = 1099511628211
)

type revision [16]byte

type storedDoc struct {
	json     []byte
	revision revision
}

type store struct {
	db *pebble.DB

	// Database locks precede document locks so deletion excludes active writes.
	dbStripes  [dbStripeCount]sync.RWMutex
	docStripes [docStripeCount]sync.Mutex
}

func storeOptions() *pebble.Options {
	return &pebble.Options{
		Levels: []pebble.LevelOptions{{
			FilterPolicy: bloom.FilterPolicy(10),
		}},
	}
}

func openStore(path string) (*store, error) {
	db, err := pebble.Open(path, storeOptions())
	if err != nil {
		return nil, fmt.Errorf("opening Pebble: %w", err)
	}

	return &store{db: db}, nil
}

func (s *store) close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("closing Pebble: %w", err)
	}

	return nil
}

func (s *store) createDB(name string, defs ...indexDef) (createErr error) {
	if err := validateIndexes(defs); err != nil {
		return err
	}

	dbMu := s.dbLock(name)
	dbMu.Lock()
	defer dbMu.Unlock()

	exists, err := s.hasDB(name)
	if err != nil {
		return fmt.Errorf("checking database: %w", err)
	}
	if exists {
		return errDBExists
	}

	batch := s.db.NewBatch()
	defer func() {
		if err := batch.Close(); err != nil {
			createErr = errors.Join(createErr, wrapStore("closing database batch", err))
		}
	}()

	if err := batch.Set(dbKey(name), []byte{recordVersion}, nil); err != nil {
		return wrapStore("queuing database", err)
	}
	for _, def := range defs {
		if err := batch.Set(indexDefKey(name, def.name), encodeIndexDef(def), nil); err != nil {
			return wrapStore("queuing index definition", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return wrapStore("committing database", err)
	}

	return nil
}

func (s *store) listDBs(limit int, cursor string) (names []string, listErr error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: dbPrefix,
		UpperBound: prefixEnd(dbPrefix),
	})
	if err != nil {
		return nil, wrapStore("creating database iterator", err)
	}
	defer func() {
		if err := iter.Close(); err != nil {
			listErr = errors.Join(listErr, wrapStore("closing database iterator", err))
		}
	}()

	valid := iter.First()
	if cursor != "" {
		key := dbKey(cursor)
		valid = iter.SeekGE(key)
		if valid && bytes.Equal(iter.Key(), key) {
			valid = iter.Next()
		}
	}

	for ; valid && len(names) < limit; valid = iter.Next() {
		name := string(iter.Key()[len(dbPrefix):])
		if !validDBRecord(iter.Value()) {
			return nil, fmt.Errorf("%w: database %q", errCorruptData, name)
		}

		names = append(names, name)
	}
	if err := iter.Error(); err != nil {
		return nil, wrapStore("iterating databases", err)
	}

	return names, nil
}

func (s *store) deleteDB(name string) (deleteErr error) {
	dbMu := s.dbLock(name)
	dbMu.Lock()
	defer dbMu.Unlock()

	exists, err := s.hasDB(name)
	if err != nil {
		return fmt.Errorf("checking database: %w", err)
	}
	if !exists {
		return errDBNotFound
	}

	batch := s.db.NewBatch()
	defer func() {
		if err := batch.Close(); err != nil {
			deleteErr = errors.Join(deleteErr, wrapStore("closing deletion batch", err))
		}
	}()

	if err := batch.Delete(dbKey(name), nil); err != nil {
		return wrapStore("queuing database deletion", err)
	}
	prefix := docsPrefix(name)
	if err := batch.DeleteRange(prefix, prefixEnd(prefix), nil); err != nil {
		return wrapStore("queuing document deletion", err)
	}
	prefix = indexDataPrefix(name)
	if err := batch.DeleteRange(prefix, prefixEnd(prefix), nil); err != nil {
		return wrapStore("queuing index deletion", err)
	}
	prefix = indexDefPrefix(name)
	if err := batch.DeleteRange(prefix, prefixEnd(prefix), nil); err != nil {
		return wrapStore("queuing index definition deletion", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return wrapStore("committing database deletion", err)
	}

	return nil
}

func (s *store) createDoc(database, id string, json []byte) (rev revision, createErr error) {
	dbMu := s.dbLock(database)
	dbMu.RLock()
	defer dbMu.RUnlock()

	docMu := s.docLock(database, id)
	docMu.Lock()
	defer docMu.Unlock()

	exists, err := s.hasDB(database)
	if err != nil {
		return revision{}, fmt.Errorf("checking database: %w", err)
	}
	if !exists {
		return revision{}, errDBNotFound
	}

	key := docKey(database, id)
	exists, err = s.has(key)
	if err != nil {
		return revision{}, fmt.Errorf("checking document: %w", err)
	}
	if exists {
		return revision{}, errDocExists
	}

	defs, err := s.indexes(database)
	if err != nil {
		return revision{}, err
	}
	values, err := indexValues(json, defs)
	if err != nil {
		return revision{}, err
	}
	rev, err = makeRevision(nil)
	if err != nil {
		return revision{}, err
	}

	batch := s.db.NewBatch()
	defer func() {
		if err := batch.Close(); err != nil {
			createErr = errors.Join(createErr, wrapStore("closing document batch", err))
		}
	}()

	if err := batch.Set(key, encodeDoc(json, rev), nil); err != nil {
		return revision{}, wrapStore("queuing document", err)
	}
	if err := setIndexEntries(batch, database, id, values); err != nil {
		return revision{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return revision{}, wrapStore("committing document", err)
	}

	return rev, nil
}

func (s *store) getDoc(database, id string) (storedDoc, error) {
	return s.readDoc(docKey(database, id))
}

func (s *store) listDocs(database string, limit int, cursor string) (ids []string, more bool, listErr error) {
	dbMu := s.dbLock(database)
	dbMu.RLock()
	defer dbMu.RUnlock()

	exists, err := s.hasDB(database)
	if err != nil {
		return nil, false, fmt.Errorf("checking database: %w", err)
	}
	if !exists {
		return nil, false, errDBNotFound
	}

	prefix := docsPrefix(database)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, false, wrapStore("creating document iterator", err)
	}
	defer func() {
		if err := iter.Close(); err != nil {
			listErr = errors.Join(listErr, wrapStore("closing document iterator", err))
		}
	}()

	valid := iter.First()
	if cursor != "" {
		key := docKey(database, cursor)
		valid = iter.SeekGE(key)
		if valid && bytes.Equal(iter.Key(), key) {
			valid = iter.Next()
		}
	}

	for ; valid && len(ids) <= limit; valid = iter.Next() {
		id := string(iter.Key()[len(prefix):])
		if _, err := decodeDoc(iter.Value()); err != nil {
			return nil, false, fmt.Errorf("%w: document %q: %v", errCorruptData, id, err)
		}

		ids = append(ids, id)
	}
	if err := iter.Error(); err != nil {
		return nil, false, wrapStore("iterating documents", err)
	}
	if len(ids) > limit {
		return ids[:limit], true, nil
	}

	return ids, false, nil
}

func (s *store) replaceDoc(database, id string, json []byte, match matchCond) (rev revision, replaceErr error) {
	dbMu := s.dbLock(database)
	dbMu.RLock()
	defer dbMu.RUnlock()

	docMu := s.docLock(database, id)
	docMu.Lock()
	defer docMu.Unlock()

	key := docKey(database, id)
	current, err := s.readDoc(key)
	if err != nil {
		return revision{}, err
	}
	if !matchRevision(match, current.revision) {
		return revision{}, errPreconditionFailed
	}

	defs, err := s.indexes(database)
	if err != nil {
		return revision{}, err
	}
	oldValues, err := indexValues(current.json, defs)
	if err != nil {
		return revision{}, fmt.Errorf("%w: %v", errCorruptData, err)
	}
	newValues, err := indexValues(json, defs)
	if err != nil {
		return revision{}, err
	}
	rev, err = makeRevision(&current.revision)
	if err != nil {
		return revision{}, err
	}

	batch := s.db.NewBatch()
	defer func() {
		if err := batch.Close(); err != nil {
			replaceErr = errors.Join(replaceErr, wrapStore("closing replacement batch", err))
		}
	}()

	if err := batch.Set(key, encodeDoc(json, rev), nil); err != nil {
		return revision{}, wrapStore("queuing document replacement", err)
	}
	if err := changeIndexEntries(batch, database, id, oldValues, newValues); err != nil {
		return revision{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return revision{}, wrapStore("committing document replacement", err)
	}

	return rev, nil
}

func (s *store) patchDoc(database, id string, match matchCond, apply func([]byte) ([]byte, error)) (doc storedDoc, patchErr error) {
	dbMu := s.dbLock(database)
	dbMu.RLock()
	defer dbMu.RUnlock()

	docMu := s.docLock(database, id)
	docMu.Lock()
	defer docMu.Unlock()

	key := docKey(database, id)
	current, err := s.readDoc(key)
	if err != nil {
		return storedDoc{}, err
	}
	if !matchRevision(match, current.revision) {
		return storedDoc{}, errPreconditionFailed
	}

	json, err := apply(current.json)
	if err != nil {
		return storedDoc{}, err
	}
	defs, err := s.indexes(database)
	if err != nil {
		return storedDoc{}, err
	}
	oldValues, err := indexValues(current.json, defs)
	if err != nil {
		return storedDoc{}, fmt.Errorf("%w: %v", errCorruptData, err)
	}
	newValues, err := indexValues(json, defs)
	if err != nil {
		return storedDoc{}, err
	}
	rev, err := makeRevision(&current.revision)
	if err != nil {
		return storedDoc{}, err
	}
	doc = storedDoc{json: json, revision: rev}

	batch := s.db.NewBatch()
	defer func() {
		if err := batch.Close(); err != nil {
			patchErr = errors.Join(patchErr, wrapStore("closing patch batch", err))
		}
	}()

	if err := batch.Set(key, encodeDoc(json, rev), nil); err != nil {
		return storedDoc{}, wrapStore("queuing patched document", err)
	}
	if err := changeIndexEntries(batch, database, id, oldValues, newValues); err != nil {
		return storedDoc{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return storedDoc{}, wrapStore("committing patched document", err)
	}

	return doc, nil
}

func (s *store) deleteDoc(database, id string, match matchCond) (deleteErr error) {
	dbMu := s.dbLock(database)
	dbMu.RLock()
	defer dbMu.RUnlock()

	docMu := s.docLock(database, id)
	docMu.Lock()
	defer docMu.Unlock()

	key := docKey(database, id)
	current, err := s.readDoc(key)
	if err != nil {
		return err
	}
	if !matchRevision(match, current.revision) {
		return errPreconditionFailed
	}

	defs, err := s.indexes(database)
	if err != nil {
		return err
	}
	values, err := indexValues(current.json, defs)
	if err != nil {
		return fmt.Errorf("%w: %v", errCorruptData, err)
	}

	batch := s.db.NewBatch()
	defer func() {
		if err := batch.Close(); err != nil {
			deleteErr = errors.Join(deleteErr, wrapStore("closing document deletion batch", err))
		}
	}()

	if err := batch.Delete(key, nil); err != nil {
		return wrapStore("queuing document deletion", err)
	}
	if err := deleteIndexEntries(batch, database, id, values); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return wrapStore("committing document deletion", err)
	}

	return nil
}

func matchRevision(match matchCond, current revision) bool {
	return !match.set || match.wildcard || match.revision == current
}

func (s *store) dbLock(database string) *sync.RWMutex {
	return &s.dbStripes[hashPart(fnvOffset, database)&(dbStripeCount-1)]
}

func (s *store) docLock(database, id string) *sync.Mutex {
	hash := hashPart(fnvOffset, database)
	hash = hashPart(hashPart(hash, "\x00"), id)

	return &s.docStripes[hash&(docStripeCount-1)]
}

func hashPart(hash uint64, value string) uint64 {
	for i := range len(value) {
		hash ^= uint64(value[i])
		hash *= fnvPrime
	}

	return hash
}

func (s *store) hasDB(name string) (bool, error) {
	value, closer, err := s.db.Get(dbKey(name))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, wrapStore("reading database", err)
	}
	if !validDBRecord(value) {
		err = fmt.Errorf("%w: database %q", errCorruptData, name)
	}
	if closeErr := closer.Close(); closeErr != nil {
		err = errors.Join(err, wrapStore("closing database value", closeErr))
	}

	return true, err
}

func (s *store) has(key []byte) (bool, error) {
	_, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, wrapStore("reading value", err)
	}
	if err := closer.Close(); err != nil {
		return false, wrapStore("closing value", err)
	}

	return true, nil
}

func (s *store) readDoc(key []byte) (doc storedDoc, readErr error) {
	value, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return storedDoc{}, errDocNotFound
	}
	if err != nil {
		return storedDoc{}, wrapStore("reading document", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			readErr = errors.Join(readErr, wrapStore("closing document value", err))
		}
	}()

	doc, err = decodeDoc(value)
	if err != nil {
		return storedDoc{}, fmt.Errorf("%w: %v", errCorruptData, err)
	}

	return doc, nil
}

func validDBRecord(value []byte) bool {
	return len(value) == 1 && value[0] == recordVersion
}

func makeRevision(previous *revision) (revision, error) {
	for {
		var rev revision
		if _, err := rand.Read(rev[:]); err != nil {
			return revision{}, fmt.Errorf("generating revision: %w", err)
		}
		if previous == nil || rev != *previous {
			return rev, nil
		}
	}
}

// A versioned binary header keeps metadata outside user JSON.
func encodeDoc(json []byte, rev revision) []byte {
	value := make([]byte, 1+len(rev)+len(json))
	value[0] = recordVersion
	copy(value[1:], rev[:])
	copy(value[1+len(rev):], json)

	return value
}

func decodeDoc(value []byte) (storedDoc, error) {
	if len(value) < 1+len(revision{}) || value[0] != recordVersion {
		return storedDoc{}, errors.New("invalid document record")
	}

	var rev revision
	copy(rev[:], value[1:1+len(rev)])
	json := append([]byte(nil), value[1+len(rev):]...)

	return storedDoc{json: json, revision: rev}, nil
}

func dbKey(name string) []byte {
	return appendKey(dbPrefix, name)
}

func docsPrefix(database string) []byte {
	key := appendKey(docPrefix, database)

	return append(key, 0)
}

func docKey(database, id string) []byte {
	return append(docsPrefix(database), id...)
}

func appendKey(prefix []byte, value string) []byte {
	key := make([]byte, 0, len(prefix)+len(value))
	key = append(key, prefix...)

	return append(key, value...)
}

func wrapStore(action string, err error) error {
	kind := errStoreUnavailable
	if pebble.IsCorruptionError(err) {
		kind = errCorruptData
	}

	return errors.Join(kind, fmt.Errorf("%s: %w", action, err))
}

func isStoreUnavailable(err error) bool {
	return errors.Is(err, errStoreUnavailable) || errors.Is(err, pebble.ErrClosed)
}

func prefixEnd(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xff {
			end[i]++

			return end[:i+1]
		}
	}

	return nil
}
