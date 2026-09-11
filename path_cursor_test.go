package main

import (
	"bytes"
	"testing"
)

func TestPathCursorRoundTrip(t *testing.T) {
	t.Parallel()

	box := testQueryCipher(t)
	value := valueForCursor(t, 10)
	cursor := pathCursor{
		method: queryMethod, dialect: dialectJSONPath, order: orderIndex,
		database: "users", path: `$["age"]`, index: "age",
		op: cmpGE, value: value, lastValue: value, id: testCursorID,
	}

	token, err := encodePathCursor(box, cursor)
	if err != nil {
		t.Fatalf("encodePathCursor() error = %v", err)
	}
	got, err := decodePathCursor(box, token)
	if err != nil {
		t.Fatalf("decodePathCursor() error = %v", err)
	}
	if got.method != cursor.method || got.dialect != cursor.dialect || got.order != cursor.order || got.database != cursor.database || got.path != cursor.path || got.index != cursor.index || got.op != cursor.op || !bytes.Equal(got.value, cursor.value) || !bytes.Equal(got.lastValue, cursor.lastValue) || got.id != cursor.id {
		t.Errorf("decoded cursor = %+v, want %+v", got, cursor)
	}
}

func TestPathCursorBinding(t *testing.T) {
	t.Parallel()

	box := testQueryCipher(t)
	value := valueForCursor(t, 10)
	cursor := pathCursor{
		method: queryMethod, dialect: dialectJSONPath, order: orderDocument,
		database: "users", path: `$["age"]`, op: cmpGE, value: value, id: testCursorID,
	}
	token, err := encodePathCursor(box, cursor)
	if err != nil {
		t.Fatalf("encodePathCursor() error = %v", err)
	}
	decoded, err := decodePathCursor(box, token)
	if err != nil {
		t.Fatalf("decodePathCursor() error = %v", err)
	}

	plan := pathPlan{text: cursor.path}
	query := pathQuery{op: cursor.op, value: 10}
	if err := matchPathCursor(decoded, "users", plan, query, value); err != nil {
		t.Fatalf("matchPathCursor() error = %v", err)
	}

	tests := []struct {
		name     string
		database string
		plan     pathPlan
		query    pathQuery
		value    []byte
	}{
		{name: "database", database: "other", plan: plan, query: query, value: value},
		{name: "path", database: "users", plan: pathPlan{text: `$["name"]`}, query: query, value: value},
		{name: "order", database: "users", plan: pathPlan{text: cursor.path, index: "age"}, query: query, value: value},
		{name: "operator", database: "users", plan: plan, query: pathQuery{op: cmpGT}, value: value},
		{name: "value", database: "users", plan: plan, query: query, value: valueForCursor(t, 11)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := matchPathCursor(decoded, tt.database, tt.plan, tt.query, tt.value); err == nil {
				t.Error("matchPathCursor() error = nil")
			}
		})
	}
}
