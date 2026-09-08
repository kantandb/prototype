package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentLifecycleHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db"}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"db"}`)

	res = sendRequest(t, server, http.MethodPost, "/db/", ` { "z": 1, "a": true } `, "application/json; charset=utf-8")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", res.StatusCode, http.StatusCreated, readResponse(t, res))
	}
	etag := res.Header.Get("ETag")
	if _, err := parseETag(etag); err != nil {
		t.Errorf("ETag = %q: %v", etag, err)
	}
	var created docResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	if err := validateID(created.ID); err != nil {
		t.Errorf("created ID = %q: %v", created.ID, err)
	}
	if got, want := res.Header.Get("Location"), "/db/"+created.ID; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	res = sendRequest(t, server, http.MethodGet, "/db/"+created.ID, "", "")
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := res.Header.Get("ETag"); got != etag {
		t.Errorf("ETag = %q, want %q", got, etag)
	}
	checkResponse(t, res, http.StatusOK, `{"a":true,"z":1}`)

	res = sendRequest(t, server, http.MethodDelete, "/db/"+created.ID, "", "")
	checkResponse(t, res, http.StatusNoContent, "")

	res = sendRequest(t, server, http.MethodGet, "/db/"+created.ID, "", "")
	checkResponse(t, res, http.StatusNotFound, `{"error":{"code":"document_not_found","message":"Document does not exist"}}`)
}

func TestCreateDocumentValidationHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		contentType string
		maxBytes    int64
		status      int
		code        string
	}{
		{name: "missing content type", body: `{}`, maxBytes: 100, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "wrong content type", body: `{}`, contentType: "text/plain", maxBytes: 100, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "malformed JSON", body: `{"open":`, contentType: "application/json", maxBytes: 100, status: http.StatusBadRequest, code: "invalid_document"},
		{name: "array", body: `[]`, contentType: "application/json", maxBytes: 100, status: http.StatusBadRequest, code: "invalid_document"},
		{name: "body too large", body: strings.Repeat("x", 14), contentType: "application/json", maxBytes: 13, status: http.StatusRequestEntityTooLarge, code: "content_too_large"},
		{name: "document too large", body: `{"x":"<"}`, contentType: "application/json", maxBytes: 13, status: http.StatusRequestEntityTooLarge, code: "content_too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := newTestServer(t, tt.maxBytes)
			res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db"}`, "application/json")
			checkResponse(t, res, http.StatusCreated, `{"name":"db"}`)

			res = sendRequest(t, server, http.MethodPost, "/db/", tt.body, tt.contentType)
			body := readResponse(t, res)
			if res.StatusCode != tt.status {
				t.Errorf("status = %d, want %d; body = %s", res.StatusCode, tt.status, body)
			}
			if !strings.Contains(body, `"code":"`+tt.code+`"`) {
				t.Errorf("body = %s, want code %q", body, tt.code)
			}
		})
	}
}

func TestDocumentMissingResourcesAndPathsHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	id := "01950000-0000-7000-8000-000000000001"

	res := sendRequest(t, server, http.MethodPost, "/missing/", `{}`, "application/json")
	checkResponse(t, res, http.StatusNotFound, `{"error":{"code":"database_not_found","message":"Database does not exist"}}`)

	res = sendRequest(t, server, http.MethodGet, "/missing/"+id, "", "")
	checkResponse(t, res, http.StatusNotFound, `{"error":{"code":"document_not_found","message":"Document does not exist"}}`)

	res = sendRequest(t, server, http.MethodDelete, "/missing/not-an-id", "", "")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_id","message":"Document ID is invalid"}}`)

	res = sendRequest(t, server, http.MethodGet, "/Bad/"+id, "", "")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_name","message":"Database name is invalid"}}`)
}

func TestDocumentPersistenceHTTP(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	server := httptest.NewServer(newHandler(store, defaultMaxBodyBytes))

	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db"}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"db"}`)
	res = sendRequest(t, server, http.MethodPost, "/db/", `{"saved":true}`, "application/json")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", res.StatusCode, http.StatusCreated, readResponse(t, res))
	}
	var created docResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	server.Close()
	if err := store.close(); err != nil {
		t.Fatalf("store.close() error = %v", err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	server = httptest.NewServer(newHandler(store, defaultMaxBodyBytes))
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("store.close() error = %v", err)
		}
	})
	t.Cleanup(server.Close)

	res = sendRequest(t, server, http.MethodGet, "/db/"+created.ID, "", "")
	checkResponse(t, res, http.StatusOK, `{"saved":true}`)
	res = sendRequest(t, server, http.MethodDelete, "/db/"+created.ID, "", "")
	checkResponse(t, res, http.StatusNoContent, "")
}
