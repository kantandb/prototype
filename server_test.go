package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWelcome(t *testing.T) {
	t.Parallel()

	store, err := openStore(t.TempDir(), testMasterKey)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("store.close() error = %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	newHandler(store, defaultMaxBodyBytes).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", res.Code, http.StatusOK)
	}
	want := fmt.Sprintf(`{"name":"KantanDB","version":%q}`, version())
	if got := res.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestHealth(t *testing.T) {
	t.Parallel()

	store, err := openStore(t.TempDir(), testMasterKey)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("store.close() error = %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	newHandler(store, defaultMaxBodyBytes).ServeHTTP(res, req)

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
