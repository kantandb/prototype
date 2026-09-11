package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

func TestRoutingErrorsUseEnvelope(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)

	res := sendRequest(t, server, http.MethodGet, "/missing/route/extra", "", "")
	checkResponse(t, res, http.StatusNotFound, `{"error":{"code":"route_not_found","message":"Route does not exist"}}`)

	res = sendRequest(t, server, http.MethodPatch, "/", "", "")
	checkResponse(t, res, http.StatusMethodNotAllowed, `{"error":{"code":"method_not_allowed","message":"Method is not allowed"}}`)

	res = sendRequest(t, server, http.MethodGet, "/healthz/", "", "")
	checkResponse(t, res, http.StatusMethodNotAllowed, `{"error":{"code":"method_not_allowed","message":"Method is not allowed"}}`)
}

func TestStoppingReturnsUnavailable(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	a := newAPI(store, defaultMaxBodyBytes, slog.New(slog.DiscardHandler))
	handler := a.handler()
	a.stop()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", res.Code, http.StatusServiceUnavailable)
	}
	if got := res.Body.String(); got != `{"error":{"code":"service_unavailable","message":"Service is unavailable"}}` {
		t.Errorf("body = %q", got)
	}
}

func TestCorruptionIsMappedAndLogged(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if err := store.createDB("db"); err != nil {
		t.Fatalf("createDB() error = %v", err)
	}
	id := "01950000-0000-7000-8000-000000000001"
	if err := store.db.Set(docKey("db", id), []byte{0xff}, pebble.Sync); err != nil {
		t.Fatalf("DB.Set() error = %v", err)
	}

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	server := httptest.NewServer(newAPI(store, defaultMaxBodyBytes, log).handler())
	t.Cleanup(server.Close)

	res := sendRequest(t, server, http.MethodGet, "/db", "", "")
	checkResponse(t, res, http.StatusInternalServerError, `{"error":{"code":"corrupt_data","message":"Stored data is corrupt"}}`)
	if !strings.Contains(logs.String(), `"operation":"list documents"`) {
		t.Errorf("list log = %s", logs.String())
	}

	res = sendRequest(t, server, http.MethodGet, "/db/"+id, "", "")
	body := readResponse(t, res)
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusInternalServerError)
	}
	if body != `{"error":{"code":"corrupt_data","message":"Stored data is corrupt"}}` {
		t.Errorf("body = %q", body)
	}
	if strings.Contains(body, "invalid document record") {
		t.Errorf("response exposed internal error: %s", body)
	}
	if !strings.Contains(logs.String(), `"operation":"read document"`) || !strings.Contains(logs.String(), "invalid document record") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestClosedStorageReturnsUnavailable(t *testing.T) {
	t.Parallel()

	store, err := openStore(t.TempDir(), testMasterKey)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if err := store.close(); err != nil {
		t.Fatalf("store.close() error = %v", err)
	}

	server := httptest.NewServer(newHandler(store, defaultMaxBodyBytes))
	t.Cleanup(server.Close)
	id := "01950000-0000-7000-8000-000000000001"

	res := sendRequest(t, server, http.MethodGet, "/db", "", "")
	checkResponse(t, res, http.StatusServiceUnavailable, `{"error":{"code":"service_unavailable","message":"Service is unavailable"}}`)

	res = sendRequest(t, server, http.MethodGet, "/db/"+id, "", "")
	checkResponse(t, res, http.StatusServiceUnavailable, `{"error":{"code":"service_unavailable","message":"Service is unavailable"}}`)
}
