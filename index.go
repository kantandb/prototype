package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

var (
	errIndexNotFound       = errors.New("index not found")
	errInvalidIndex        = errors.New("invalid index")
	errInvalidIndexValue   = errors.New("invalid index value")
	errUnsupportedIdxValue = errors.New("unsupported index value")
)

const (
	maxIndexes    = 16
	maxIndexPath  = 256
	maxIndexValue = 4 << 10
)

type pathDialect byte

const pathJSONPointer pathDialect = iota

type queryPath struct {
	dialect pathDialect
	value   string
}

type predicate struct {
	path  queryPath
	op    cmpOp
	value []byte
}

type indexDef struct {
	name string
	path string
}

func validateIndexes(defs []indexDef) error {
	if len(defs) > maxIndexes {
		return fmt.Errorf("%w: too many indexes", errInvalidIndex)
	}

	seen := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		if err := validateName(def.name); err != nil {
			return fmt.Errorf("%w: invalid name %q", errInvalidIndex, def.name)
		}
		if err := validatePointer(def.path); err != nil {
			return fmt.Errorf("%w: path %q: %v", errInvalidIndex, def.path, err)
		}
		if _, ok := seen[def.name]; ok {
			return fmt.Errorf("%w: duplicate name %q", errInvalidIndex, def.name)
		}
		seen[def.name] = struct{}{}
	}

	return nil
}

func validatePointer(path string) error {
	if len(path) == 0 || len(path) > maxIndexPath || path[0] != '/' || !utf8.ValidString(path) {
		return errors.New("invalid JSON Pointer")
	}

	for i := 0; i < len(path); i++ {
		if path[i] != '~' {
			continue
		}
		if i+1 == len(path) || path[i+1] != '0' && path[i+1] != '1' {
			return errors.New("invalid JSON Pointer escape")
		}
		i++
	}

	return nil
}

func indexValues(document []byte, defs []indexDef) (map[string][]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()

	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("decoding indexed document: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("decoding indexed document: %w", err)
	}

	values := make(map[string][]byte, len(defs))
	for _, def := range defs {
		value, ok := pointerValue(root, def.path)
		if !ok {
			continue
		}

		encoded, err := encodeIndexValue(value)
		if errors.Is(err, errUnsupportedIdxValue) {
			continue
		}
		if err != nil {
			return nil, err
		}
		values[def.name] = encoded
	}

	return values, nil
}

func pointerValue(root map[string]any, path string) (any, bool) {
	var current any = root
	for token := range strings.SplitSeq(path[1:], "/") {
		name := strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")

		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[name]
			if !ok {
				return nil, false
			}
		case []any:
			// Array tokens use canonical decimal indices.
			index, err := strconv.Atoi(name)
			if err != nil || index < 0 || strconv.Itoa(index) != name || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}

	return current, true
}

func encodeIndexValue(value any) ([]byte, error) {
	var encoded []byte

	switch value := value.(type) {
	case nil:
		encoded = []byte{0x00}
	case bool:
		if value {
			encoded = []byte{0x02}
		} else {
			encoded = []byte{0x01}
		}
	case json.Number:
		literal := string(value)
		if literal == "" {
			return nil, fmt.Errorf("%w: invalid number", errInvalidIndexValue)
		}
		// Marshal validates JSON number syntax.
		if _, err := json.Marshal(value); err != nil {
			return nil, fmt.Errorf("%w: invalid number", errInvalidIndexValue)
		}

		number, ok := new(big.Rat).SetString(literal)
		if !ok {
			return nil, fmt.Errorf("%w: invalid number", errInvalidIndexValue)
		}
		encoded = append([]byte{0x03}, number.RatString()...)
	case string:
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("%w: invalid UTF-8", errInvalidIndexValue)
		}
		encoded = append([]byte{0x04}, value...)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return encodeQueryNumber(value)
	default:
		return nil, fmt.Errorf("%w: %w", errInvalidIndexValue, errUnsupportedIdxValue)
	}

	if len(encoded) > maxIndexValue {
		return nil, fmt.Errorf("%w: value is too large", errInvalidIndexValue)
	}

	return encoded, nil
}

func encodeQueryNumber(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidIndexValue, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var number json.Number
	if err := decoder.Decode(&number); err != nil {
		return nil, fmt.Errorf("%w: value must be scalar", errInvalidIndexValue)
	}

	return encodeIndexValue(number)
}

func appendPart(key []byte, part []byte) []byte {
	key = binary.AppendUvarint(key, uint64(len(part)))

	return append(key, part...)
}

func readPart(key []byte) (part, rest []byte, ok bool) {
	length, size := binary.Uvarint(key)
	if size <= 0 || length > uint64(len(key)-size) {
		return nil, nil, false
	}

	end := size + int(length)

	return key[size:end], key[end:], true
}

func indexDefPrefix(database string) []byte {
	return appendPart(append([]byte(nil), idxDefPrefix...), []byte(database))
}

func indexDefKey(database, index string) []byte {
	return appendPart(indexDefPrefix(database), []byte(index))
}

func indexDataPrefix(database string) []byte {
	return appendPart(append([]byte(nil), idxDataPrefix...), []byte(database))
}

func indexValuePrefix(database, index string, value []byte) []byte {
	key := appendPart(indexDataPrefix(database), []byte(index))

	return appendPart(key, value)
}

func indexKey(database, index string, value []byte, id string) []byte {
	return append(indexValuePrefix(database, index, value), id...)
}

func encodeIndexDef(def indexDef) []byte {
	return append([]byte{recordVersion}, def.path...)
}

func decodeIndexDef(name string, value []byte) (indexDef, error) {
	if len(value) < 2 || value[0] != recordVersion {
		return indexDef{}, errors.New("invalid index definition")
	}

	def := indexDef{name: name, path: string(value[1:])}
	if err := validateIndexes([]indexDef{def}); err != nil {
		return indexDef{}, err
	}

	return def, nil
}

func (s *store) indexes(database string) (defs []indexDef, loadErr error) {
	prefix := indexDefPrefix(database)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, wrapStore("creating index iterator", err)
	}
	defer func() {
		if err := iter.Close(); err != nil {
			loadErr = errors.Join(loadErr, wrapStore("closing index iterator", err))
		}
	}()

	for valid := iter.First(); valid; valid = iter.Next() {
		namePart, rest, ok := readPart(iter.Key()[len(prefix):])
		if !ok || len(rest) != 0 {
			return nil, fmt.Errorf("%w: invalid index definition key", errCorruptData)
		}

		def, err := decodeIndexDef(string(namePart), iter.Value())
		if err != nil {
			return nil, fmt.Errorf("%w: index %q: %v", errCorruptData, namePart, err)
		}
		defs = append(defs, def)
	}
	if err := iter.Error(); err != nil {
		return nil, wrapStore("iterating indexes", err)
	}
	if err := validateIndexes(defs); err != nil {
		return nil, fmt.Errorf("%w: %v", errCorruptData, err)
	}

	slices.SortFunc(defs, func(a, b indexDef) int { return bytes.Compare([]byte(a.name), []byte(b.name)) })

	return defs, nil
}

func findIndex(defs []indexDef, name string) (indexDef, bool) {
	for _, def := range defs {
		if def.name == name {
			return def, true
		}
	}

	return indexDef{}, false
}

func setIndexEntries(batch *pebble.Batch, database, id string, values map[string][]byte) error {
	for name, value := range values {
		if err := batch.Set(indexKey(database, name, value, id), nil, nil); err != nil {
			return wrapStore("queuing index entry", err)
		}
	}

	return nil
}

func changeIndexEntries(batch *pebble.Batch, database, id string, old, next map[string][]byte) error {
	for name, value := range old {
		if bytes.Equal(value, next[name]) {
			continue
		}
		if err := batch.Delete(indexKey(database, name, value, id), nil); err != nil {
			return wrapStore("queuing index deletion", err)
		}
	}

	for name, value := range next {
		if bytes.Equal(value, old[name]) {
			continue
		}
		if err := batch.Set(indexKey(database, name, value, id), nil, nil); err != nil {
			return wrapStore("queuing index entry", err)
		}
	}

	return nil
}

func deleteIndexEntries(batch *pebble.Batch, database, id string, values map[string][]byte) error {
	for name, value := range values {
		if err := batch.Delete(indexKey(database, name, value, id), nil); err != nil {
			return wrapStore("queuing index deletion", err)
		}
	}

	return nil
}

func (s *store) queryDocs(database, index string, encoded []byte, limit int, cursor string) (ids []string, more bool, queryErr error) {
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

	defs, err := s.indexes(database)
	if err != nil {
		return nil, false, err
	}
	def, ok := findIndex(defs, index)
	if !ok {
		return nil, false, errIndexNotFound
	}

	pred := predicate{
		path:  queryPath{dialect: pathJSONPointer, value: def.path},
		op:    cmpEq,
		value: encoded,
	}
	if !validIndexValue(pred.value) {
		return nil, false, errInvalidIndexValue
	}
	prefix := indexValuePrefix(database, index, pred.value)
	snapshot := s.db.NewSnapshot()
	defer func() {
		if err := snapshot.Close(); err != nil {
			queryErr = errors.Join(queryErr, wrapStore("closing query snapshot", err))
		}
	}()

	iter, err := snapshot.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, false, wrapStore("creating query iterator", err)
	}
	defer func() {
		if err := iter.Close(); err != nil {
			queryErr = errors.Join(queryErr, wrapStore("closing query iterator", err))
		}
	}()

	valid := iter.First()
	if cursor != "" {
		key := append(append([]byte(nil), prefix...), cursor...)
		valid = iter.SeekGE(key)
		if valid && bytes.Equal(iter.Key(), key) {
			valid = iter.Next()
		}
	}

	for ; valid && len(ids) <= limit; valid = iter.Next() {
		id := string(iter.Key()[len(prefix):])
		if len(iter.Value()) != 0 || validateID(id) != nil {
			return nil, false, fmt.Errorf("%w: invalid index entry", errCorruptData)
		}

		exists, err := snapshotHas(snapshot, docKey(database, id))
		if err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, fmt.Errorf("%w: index references missing document", errCorruptData)
		}
		ids = append(ids, id)
	}
	if err := iter.Error(); err != nil {
		return nil, false, wrapStore("iterating query", err)
	}
	if len(ids) > limit {
		return ids[:limit], true, nil
	}

	return ids, false, nil
}

func snapshotHas(snapshot *pebble.Snapshot, key []byte) (bool, error) {
	_, closer, err := snapshot.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, wrapStore("reading query document", err)
	}
	if err := closer.Close(); err != nil {
		return false, wrapStore("closing query document", err)
	}

	return true, nil
}
