package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestListDocumentsHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db"}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"db"}`)

	res = sendRequest(t, server, http.MethodGet, "/db", "", "")
	checkResponse(t, res, http.StatusOK, `{"documents":[],"cursor":""}`)

	var ids []string
	for range 3 {
		res = sendRequest(t, server, http.MethodPost, "/db/", `{}`, "application/json")
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
		ids = append(ids, created.ID)
	}
	slices.Sort(ids)

	res = sendRequest(t, server, http.MethodGet, "/db?limit=2", "", "")
	var page docList
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	if !slices.Equal(page.Documents, ids[:2]) {
		t.Errorf("documents = %v, want %v", page.Documents, ids[:2])
	}
	if page.Cursor != ids[1] {
		t.Errorf("cursor = %q, want %q", page.Cursor, ids[1])
	}

	res = sendRequest(t, server, http.MethodGet, "/db?limit=1&cursor="+ids[1], "", "")
	page = docList{}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	if want := ids[2:]; !slices.Equal(page.Documents, want) {
		t.Errorf("documents after cursor = %v, want %v", page.Documents, want)
	}
	if page.Cursor != "" {
		t.Errorf("final cursor = %q, want empty", page.Cursor)
	}

	res = sendRequest(t, server, http.MethodGet, "/db?cursor="+ids[2], "", "")
	checkResponse(t, res, http.StatusOK, `{"documents":[],"cursor":""}`)
}

func TestListDocumentsValidationHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db"}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"db"}`)

	tests := []struct {
		path string
		code string
	}{
		{path: "/db?limit=", code: "invalid_limit"},
		{path: "/db?limit=0", code: "invalid_limit"},
		{path: "/db?limit=1001", code: "invalid_limit"},
		{path: "/db?limit=none", code: "invalid_limit"},
		{path: "/db?cursor=bad", code: "invalid_cursor"},
		{path: "/db?unknown=true", code: "invalid_query"},
		{path: "/db?limit=1&limit=2", code: "invalid_query"},
		{path: "/db?cursor=&cursor=", code: "invalid_query"},
	}
	for _, tt := range tests {
		res = sendRequest(t, server, http.MethodGet, tt.path, "", "")
		body := readResponse(t, res)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d; body = %s", tt.path, res.StatusCode, http.StatusBadRequest, body)
		}
		if !strings.Contains(body, `"code":"`+tt.code+`"`) {
			t.Errorf("GET %s body = %s, want code %q", tt.path, body, tt.code)
		}
	}

	res = sendRequest(t, server, http.MethodGet, "/Bad", "", "")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_name","message":"Database name is invalid"}}`)

	res = sendRequest(t, server, http.MethodGet, "/missing", "", "")
	checkResponse(t, res, http.StatusNotFound, `{"error":{"code":"database_not_found","message":"Database does not exist"}}`)
}

func TestOversizedIndexedValueHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db","indexes":[{"name":"value","path":"/value"}]}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"db","indexes":[{"name":"value","path":"/value"}]}`)

	large := `{"value":"` + strings.Repeat("x", maxIndexValue) + `"}`
	res = sendRequest(t, server, http.MethodPost, "/db/", large, "application/json")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_document","message":"An indexed value exceeds the size limit"}}`)

	id := createQueryDoc(t, server, "/db/", `{"value":"ok"}`)
	res = sendRequest(t, server, http.MethodPut, "/db/"+id, large, "application/json")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_document","message":"An indexed value exceeds the size limit"}}`)

	res = sendRequest(t, server, http.MethodPatch, "/db/"+id, large, "application/merge-patch+json")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_patch","message":"An indexed value exceeds the size limit"}}`)

	res = sendRequest(t, server, http.MethodGet, "/db/"+id, "", "")
	checkResponse(t, res, http.StatusOK, `{"value":"ok"}`)
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

func TestHTTPDurabilityAcrossRestarts(t *testing.T) {
	t.Parallel()

	dataPath := t.TempDir()
	var store *store
	var server *httptest.Server
	start := func() {
		var err error
		store, err = openStore(dataPath, testMasterKey)
		if err != nil {
			t.Fatalf("openStore() error = %v", err)
		}
		server = httptest.NewServer(newHandler(store, defaultMaxBodyBytes))
	}
	stop := func() {
		server.Close()
		if err := store.close(); err != nil {
			t.Fatalf("store.close() error = %v", err)
		}
		server = nil
		store = nil
	}
	defer func() {
		if server != nil {
			stop()
		}
	}()

	start()
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db"}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"db"}`)
	res = sendRequest(t, server, http.MethodPost, "/db/", `{"stage":"created"}`, "application/json")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", res.StatusCode, http.StatusCreated, readResponse(t, res))
	}
	createdETag := res.Header.Get("ETag")
	var created docResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	docPath := "/db/" + created.ID
	stop()

	start()
	res = sendRequest(t, server, http.MethodGet, docPath, "", "")
	if got := res.Header.Get("ETag"); got != createdETag {
		t.Errorf("created ETag after restart = %q, want %q", got, createdETag)
	}
	checkResponse(t, res, http.StatusOK, `{"stage":"created"}`)
	res = sendMatchRequest(t, server, http.MethodPut, docPath, `{"stage":"replaced"}`, "application/json", createdETag)
	replacedETag := res.Header.Get("ETag")
	checkResponse(t, res, http.StatusOK, `{"stage":"replaced"}`)
	stop()

	start()
	res = sendRequest(t, server, http.MethodGet, docPath, "", "")
	if got := res.Header.Get("ETag"); got != replacedETag {
		t.Errorf("replacement ETag after restart = %q, want %q", got, replacedETag)
	}
	checkResponse(t, res, http.StatusOK, `{"stage":"replaced"}`)
	res = sendMatchRequest(t, server, http.MethodPatch, docPath, `{"merged":true}`, mergePatchType, replacedETag)
	patchedETag := res.Header.Get("ETag")
	checkResponse(t, res, http.StatusOK, `{"merged":true,"stage":"replaced"}`)
	stop()

	start()
	res = sendRequest(t, server, http.MethodGet, docPath, "", "")
	if got := res.Header.Get("ETag"); got != patchedETag {
		t.Errorf("patch ETag after restart = %q, want %q", got, patchedETag)
	}
	checkResponse(t, res, http.StatusOK, `{"merged":true,"stage":"replaced"}`)
	res = sendMatchRequest(t, server, http.MethodDelete, docPath, "", "", patchedETag)
	checkResponse(t, res, http.StatusNoContent, "")
	stop()

	start()
	res = sendRequest(t, server, http.MethodGet, docPath, "", "")
	checkResponse(t, res, http.StatusNotFound, `{"error":{"code":"document_not_found","message":"Document does not exist"}}`)
	res = sendRequest(t, server, http.MethodDelete, "/db", "", "")
	checkResponse(t, res, http.StatusNoContent, "")
	stop()

	start()
	res = sendRequest(t, server, http.MethodGet, "/", "", "")
	checkResponse(t, res, http.StatusOK, `{"databases":[]}`)
}
