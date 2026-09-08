package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
)

var (
	errDBExists    = errors.New("database exists")
	errDBNotFound  = errors.New("database not found")
	errDocExists   = errors.New("document exists")
	errDocNotFound = errors.New("document not found")
)

var (
	dbPrefix  = []byte{0x01}
	docPrefix = []byte{0x02}
)

const recordVersion byte = 1

type revision [16]byte

type storedDoc struct {
	json     []byte
	revision revision
}

type store struct {
	db *pebble.DB
	mu sync.Mutex // Serializes every mutation.
}

func openStore(path string) (*store, error) {
	db, err := pebble.Open(path, &pebble.Options{})
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

func (s *store) createDB(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	exists, err := s.hasDB(name)
	if err != nil {
		return fmt.Errorf("checking database: %w", err)
	}
	if exists {
		return errDBExists
	}

	if err := s.db.Set(dbKey(name), []byte{recordVersion}, pebble.Sync); err != nil {
		return fmt.Errorf("writing database: %w", err)
	}

	return nil
}

func (s *store) listDBs() (names []string, listErr error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: dbPrefix,
		UpperBound: prefixEnd(dbPrefix),
	})
	if err != nil {
		return nil, fmt.Errorf("creating database iterator: %w", err)
	}
	defer func() {
		if err := iter.Close(); err != nil {
			listErr = errors.Join(listErr, fmt.Errorf("closing database iterator: %w", err))
		}
	}()

	for iter.First(); iter.Valid(); iter.Next() {
		name := string(iter.Key()[len(dbPrefix):])
		if !validDBRecord(iter.Value()) {
			return nil, fmt.Errorf("decoding database %q: invalid database record", name)
		}

		names = append(names, name)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterating databases: %w", err)
	}

	return names, nil
}

func (s *store) deleteDB(name string) (deleteErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
			deleteErr = errors.Join(deleteErr, fmt.Errorf("closing deletion batch: %w", err))
		}
	}()

	if err := batch.Delete(dbKey(name), nil); err != nil {
		return fmt.Errorf("queuing database deletion: %w", err)
	}
	prefix := docsPrefix(name)
	if err := batch.DeleteRange(prefix, prefixEnd(prefix), nil); err != nil {
		return fmt.Errorf("queuing document deletion: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("committing database deletion: %w", err)
	}

	return nil
}

func (s *store) createDoc(database, id string, json []byte) (revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	rev, err := makeRevision(nil)
	if err != nil {
		return revision{}, err
	}
	if err := s.db.Set(key, encodeDoc(json, rev), pebble.Sync); err != nil {
		return revision{}, fmt.Errorf("writing document: %w", err)
	}

	return rev, nil
}

func (s *store) getDoc(database, id string) (storedDoc, error) {
	return s.readDoc(docKey(database, id))
}

func (s *store) replaceDoc(database, id string, json []byte) (revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := docKey(database, id)
	current, err := s.readDoc(key)
	if err != nil {
		return revision{}, err
	}

	rev, err := makeRevision(&current.revision)
	if err != nil {
		return revision{}, err
	}
	if err := s.db.Set(key, encodeDoc(json, rev), pebble.Sync); err != nil {
		return revision{}, fmt.Errorf("writing document: %w", err)
	}

	return rev, nil
}

func (s *store) deleteDoc(database, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := docKey(database, id)
	exists, err := s.has(key)
	if err != nil {
		return fmt.Errorf("checking document: %w", err)
	}
	if !exists {
		return errDocNotFound
	}

	if err := s.db.Delete(key, pebble.Sync); err != nil {
		return fmt.Errorf("deleting document: %w", err)
	}

	return nil
}

func (s *store) hasDB(name string) (bool, error) {
	value, closer, err := s.db.Get(dbKey(name))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !validDBRecord(value) {
		err = errors.New("invalid database record")
	}
	if closeErr := closer.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("closing value: %w", closeErr))
	}

	return true, err
}

func (s *store) has(key []byte) (bool, error) {
	_, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := closer.Close(); err != nil {
		return false, fmt.Errorf("closing value: %w", err)
	}

	return true, nil
}

func (s *store) readDoc(key []byte) (doc storedDoc, readErr error) {
	value, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return storedDoc{}, errDocNotFound
	}
	if err != nil {
		return storedDoc{}, fmt.Errorf("reading document: %w", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			readErr = errors.Join(readErr, fmt.Errorf("closing document value: %w", err))
		}
	}()

	doc, err = decodeDoc(value)
	if err != nil {
		return storedDoc{}, fmt.Errorf("decoding document: %w", err)
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
