package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cockroachdb/pebble"
)

func TestHealth(t *testing.T) {
	t.Parallel()

	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("DB.Close() error = %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	newHandler(db).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	if got := res.Body.String(); got != "{\"status\":\"ok\"}" {
		t.Errorf("body = %q, want %q", got, "{\"status\":\"ok\"}")
	}
}
