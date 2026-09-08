package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestReplaceAndConditionalDeleteHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	id, firstETag := createHTTPDoc(t, server, `{"value":"first"}`)
	path := "/db/" + id

	res := sendMatchRequest(t, server, http.MethodPut, path, ` { "value": "second" } `, "application/json", firstETag)
	secondETag := res.Header.Get("ETag")
	if secondETag == firstETag {
		t.Error("replacement did not change ETag")
	}
	checkResponse(t, res, http.StatusOK, `{"value":"second"}`)

	res = sendMatchRequest(t, server, http.MethodPut, path, `{"value":"stale"}`, "application/json", firstETag)
	checkResponse(t, res, http.StatusPreconditionFailed, `{"error":{"code":"precondition_failed","message":"If-Match precondition failed"}}`)

	res = sendRequest(t, server, http.MethodGet, path, "", "")
	if got := res.Header.Get("ETag"); got != secondETag {
		t.Errorf("ETag = %q, want %q", got, secondETag)
	}
	checkResponse(t, res, http.StatusOK, `{"value":"second"}`)

	res = sendMatchRequest(t, server, http.MethodPut, path, `{"value":"third"}`, "application/json", "*")
	thirdETag := res.Header.Get("ETag")
	checkResponse(t, res, http.StatusOK, `{"value":"third"}`)

	res = sendMatchRequest(t, server, http.MethodDelete, path, "", "", firstETag)
	checkResponse(t, res, http.StatusPreconditionFailed, `{"error":{"code":"precondition_failed","message":"If-Match precondition failed"}}`)

	res = sendMatchRequest(t, server, http.MethodDelete, path, "", "", thirdETag)
	checkResponse(t, res, http.StatusNoContent, "")
}

func TestReplaceDocumentValidationHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, 32)
	id, _ := createHTTPDoc(t, server, `{}`)
	path := "/db/" + id

	tests := []struct {
		name        string
		path        string
		body        string
		contentType string
		ifMatch     string
		status      int
		code        string
	}{
		{name: "missing content type", path: path, body: `{}`, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "non-object", path: path, body: `[]`, contentType: "application/json", status: http.StatusBadRequest, code: "invalid_document"},
		{name: "too large", path: path, body: strings.Repeat("x", 33), contentType: "application/json", status: http.StatusRequestEntityTooLarge, code: "content_too_large"},
		{name: "malformed If-Match", path: path, body: `{}`, contentType: "application/json", ifMatch: "bad", status: http.StatusBadRequest, code: "invalid_if_match"},
		{name: "missing document", path: "/db/01950000-0000-7000-8000-000000000001", body: `{}`, contentType: "application/json", status: http.StatusNotFound, code: "document_not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := sendMatchRequest(t, server, http.MethodPut, tt.path, tt.body, tt.contentType, tt.ifMatch)
			body := readResponse(t, res)
			if res.StatusCode != tt.status {
				t.Errorf("status = %d, want %d; body = %s", res.StatusCode, tt.status, body)
			}
			if !strings.Contains(body, `"code":"`+tt.code+`"`) {
				t.Errorf("body = %s, want code %q", body, tt.code)
			}
		})
	}

	res := sendMatchRequest(t, server, http.MethodDelete, path, "", "", "bad")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_if_match","message":"If-Match is invalid"}}`)
}

func TestConcurrentConditionalReplaceHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	id, etag := createHTTPDoc(t, server, `{"value":0}`)
	path := "/db/" + id

	const workers = 8
	statuses := make(chan int, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			body := `{"value":` + string(rune('1'+i)) + `}`
			res := sendMatchRequest(t, server, http.MethodPut, path, body, "application/json", etag)
			statuses <- res.StatusCode
			_ = readResponse(t, res)
		})
	}
	wg.Wait()
	close(statuses)

	var replaced, rejected int
	for status := range statuses {
		switch status {
		case http.StatusOK:
			replaced++
		case http.StatusPreconditionFailed:
			rejected++
		default:
			t.Errorf("status = %d, want 200 or 412", status)
		}
	}
	if replaced != 1 || rejected != workers-1 {
		t.Errorf("replaced = %d, rejected = %d", replaced, rejected)
	}

	res := sendRequest(t, server, http.MethodGet, path, "", "")
	if res.Header.Get("ETag") == etag {
		t.Error("visible document retained old ETag")
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	_ = readResponse(t, res)
}

func createHTTPDoc(t *testing.T, server *httptest.Server, body string) (string, string) {
	t.Helper()

	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"db"}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"db"}`)

	res = sendRequest(t, server, http.MethodPost, "/db/", body, "application/json")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", res.StatusCode, http.StatusCreated, readResponse(t, res))
	}
	etag := res.Header.Get("ETag")
	var created docResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}

	return created.ID, etag
}

func sendMatchRequest(t *testing.T, server *httptest.Server, method, path, body, contentType, ifMatch string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}

	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}

	return res
}
