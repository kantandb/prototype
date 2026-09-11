package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/cockroachdb/pebble"
	"github.com/theory/jsonpath"
)

const queryMethod = "QUERY"

type pathQuery struct {
	path   string
	value  any
	cursor string
	op     cmpOp
	limit  int
}

func decodePathQuery(body []byte) (pathQuery, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return pathQuery{}, errors.New("query must be an object")
	}

	query := pathQuery{op: cmpEq, limit: defaultListLimit}
	seen := make(map[string]bool, 5)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return pathQuery{}, err
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return pathQuery{}, errors.New("invalid query field")
		}
		seen[name] = true

		switch name {
		case "path":
			err = decoder.Decode(&query.path)
		case "op":
			var raw string
			if err = decoder.Decode(&raw); err == nil {
				var ok bool
				query.op, ok = parseCmpOp(raw)
				if !ok {
					err = errors.New("invalid operator")
				}
			}
		case "value":
			err = decoder.Decode(&query.value)
		case "limit":
			err = decoder.Decode(&query.limit)
		case "cursor":
			err = decoder.Decode(&query.cursor)
		default:
			return pathQuery{}, errors.New("unknown query field")
		}
		if err != nil {
			return pathQuery{}, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return pathQuery{}, err
	}
	if err := jsonEnd(decoder); err != nil {
		return pathQuery{}, err
	}
	if !seen["path"] || !seen["value"] || query.path == "" {
		return pathQuery{}, errors.New("path and value are required")
	}
	if query.limit < 1 || query.limit > maxListLimit {
		return pathQuery{}, errors.New("invalid limit")
	}
	switch query.value.(type) {
	case nil, bool, json.Number, string:
	default:
		return pathQuery{}, errors.New("value must be scalar")
	}
	if query.op != cmpEq {
		switch query.value.(type) {
		case json.Number, string:
		default:
			return pathQuery{}, errors.New("operator requires a number or string")
		}
	}

	return query, nil
}

func jsonEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}

		return err
	}

	return nil
}

func (s *store) queryPathDocs(ctx context.Context, database string, path *jsonpath.Path, op cmpOp, value any, limit int, afterID string) (ids []string, more bool, queryErr error) {
	dbMu := s.dbLock(database)
	dbMu.RLock()
	defer dbMu.RUnlock()

	databaseKey, err := s.databaseKey(database)
	if err != nil {
		return nil, false, err
	}
	defer clear(databaseKey)

	encoded, err := encodeIndexValue(value)
	if err != nil {
		return nil, false, err
	}

	prefix := docsPrefix(database)
	snapshot := s.db.NewSnapshot()
	defer func() {
		if err := snapshot.Close(); err != nil {
			queryErr = errors.Join(queryErr, wrapStore("closing path query snapshot", err))
		}
	}()

	iter, err := snapshot.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, false, wrapStore("creating path query iterator", err)
	}
	defer func() {
		if err := iter.Close(); err != nil {
			queryErr = errors.Join(queryErr, wrapStore("closing path query iterator", err))
		}
	}()

	valid := iter.First()
	if afterID != "" {
		key := docKey(database, afterID)
		valid = iter.SeekGE(key)
		if valid && bytes.Equal(iter.Key(), key) {
			valid = iter.Next()
		}
	}

	for ; valid && len(ids) <= limit; valid = iter.Next() {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}

		id := string(iter.Key()[len(prefix):])
		if validateID(id) != nil {
			return nil, false, fmt.Errorf("%w: invalid document key", errCorruptData)
		}
		doc, err := openDoc(iter.Key(), databaseKey, id, iter.Value())
		if err != nil {
			return nil, false, fmt.Errorf("reading query document: %w", err)
		}

		var root any
		decoder := json.NewDecoder(bytes.NewReader(doc.json))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			return nil, false, fmt.Errorf("%w: invalid document JSON", errCorruptData)
		}
		if pathMatches(path, root, op, encoded) {
			ids = append(ids, id)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, false, wrapStore("iterating path query", err)
	}
	if len(ids) > limit {
		return ids[:limit], true, nil
	}

	return ids, false, nil
}

func pathMatches(path *jsonpath.Path, root any, op cmpOp, expected []byte) bool {
	for _, value := range path.Select(root) {
		actual, err := encodeIndexValue(value)
		if err != nil || actual[0] != expected[0] {
			continue
		}

		order := bytes.Compare(actual, expected)
		if op == cmpEq && order == 0 || op == cmpLT && order < 0 || op == cmpLE && order <= 0 || op == cmpGT && order > 0 || op == cmpGE && order >= 0 {
			return true
		}
	}

	return false
}
