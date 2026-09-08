package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDatabaseLifecycleHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)

	res := sendRequest(t, server, http.MethodGet, "/", "", "")
	checkResponse(t, res, http.StatusOK, `{"databases":[]}`)

	res = sendRequest(t, server, http.MethodPost, "/", `{"name":"beta"}`, "application/json; charset=utf-8")
	if got := res.Header.Get("Location"); got != "/beta" {
		t.Errorf("Location = %q, want %q", got, "/beta")
	}
	checkResponse(t, res, http.StatusCreated, `{"name":"beta"}`)

	res = sendRequest(t, server, http.MethodPost, "/", `{"name":"alpha"}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"alpha"}`)

	res = sendRequest(t, server, http.MethodGet, "/", "", "")
	checkResponse(t, res, http.StatusOK, `{"databases":["alpha","beta"]}`)

	res = sendRequest(t, server, http.MethodPost, "/", `{"name":"alpha"}`, "application/json")
	checkResponse(t, res, http.StatusConflict, `{"error":{"code":"database_exists","message":"Database already exists"}}`)

	res = sendRequest(t, server, http.MethodDelete, "/alpha", "", "")
	checkResponse(t, res, http.StatusNoContent, "")

	res = sendRequest(t, server, http.MethodGet, "/", "", "")
	checkResponse(t, res, http.StatusOK, `{"databases":["beta"]}`)

	res = sendRequest(t, server, http.MethodDelete, "/alpha", "", "")
	checkResponse(t, res, http.StatusNotFound, `{"error":{"code":"database_not_found","message":"Database does not exist"}}`)
}

func TestCreateDatabaseValidationHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		contentType string
		maxBytes    int64
		status      int
		code        string
	}{
		{name: "missing content type", body: `{"name":"db"}`, maxBytes: 100, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "wrong content type", body: `{"name":"db"}`, contentType: "text/plain", maxBytes: 100, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "malformed media type", body: `{"name":"db"}`, contentType: "application/json; bad", maxBytes: 100, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "malformed JSON", body: `{"name":`, contentType: "application/json", maxBytes: 100, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "non-object JSON", body: `[]`, contentType: "application/json", maxBytes: 100, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown field", body: `{"name":"db","other":true}`, contentType: "application/json", maxBytes: 100, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "trailing value", body: `{"name":"db"}{}`, contentType: "application/json", maxBytes: 100, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid name", body: `{"name":"Bad"}`, contentType: "application/json", maxBytes: 100, status: http.StatusBadRequest, code: "invalid_name"},
		{name: "too large", body: strings.Repeat("x", 17), contentType: "application/json", maxBytes: 16, status: http.StatusRequestEntityTooLarge, code: "content_too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := newTestServer(t, tt.maxBytes)
			res := sendRequest(t, server, http.MethodPost, "/", tt.body, tt.contentType)
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

func TestDeleteDatabaseValidatesNameHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodDelete, "/Bad", "", "")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_name","message":"Database name is invalid"}}`)
}

func newTestServer(t *testing.T, maxBodyBytes int64) *httptest.Server {
	t.Helper()

	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("store.close() error = %v", err)
		}
	})

	server := httptest.NewServer(newHandler(store, maxBodyBytes))
	t.Cleanup(server.Close)

	return server
}

func sendRequest(t *testing.T, server *httptest.Server, method, path, body, contentType string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}

	return res
}

func checkResponse(t *testing.T, res *http.Response, status int, body string) {
	t.Helper()

	gotBody := readResponse(t, res)
	if res.StatusCode != status {
		t.Errorf("status = %d, want %d; body = %s", res.StatusCode, status, gotBody)
	}
	if gotBody != body {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
}

func readResponse(t *testing.T, res *http.Response) string {
	t.Helper()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("Response.Body.Close() error = %v", err)
		}
	}()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}

	return string(body)
}
