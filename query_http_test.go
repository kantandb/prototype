package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestQueryDocumentsHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"users","indexes":[{"name":"active","path":"/active"}]}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"users","indexes":[{"name":"active","path":"/active"}]}`)

	var active []string
	for _, body := range []string{`{"active":true}`, `{"active":false}`, `{"active":true}`} {
		res = sendRequest(t, server, http.MethodPost, "/users/", body, "application/json")
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
		if body == `{"active":true}` {
			active = append(active, created.ID)
		}
	}
	slices.Sort(active)

	res = sendRequest(t, server, http.MethodGet, "/users?index=active&value=true&limit=1", "", "")
	var page docList
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	if !slices.Equal(page.Documents, active[:1]) {
		t.Errorf("documents = %v, want %v", page.Documents, active[:1])
	}
	if page.Cursor == "" {
		t.Error("cursor is empty on first page")
	}

	path := "/users?index=active&value=true&limit=1&cursor=" + url.QueryEscape(page.Cursor)
	res = sendRequest(t, server, http.MethodGet, path, "", "")
	page = docList{}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	if !slices.Equal(page.Documents, active[1:]) {
		t.Errorf("final documents = %v, want %v", page.Documents, active[1:])
	}
	if page.Cursor != "" {
		t.Errorf("final cursor = %q, want empty", page.Cursor)
	}
}

func TestQueryDocumentsValidationHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"users","indexes":[{"name":"active","path":"/active"}]}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"users","indexes":[{"name":"active","path":"/active"}]}`)

	tests := []struct {
		path   string
		status int
		code   string
	}{
		{path: "/users?index=active", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?value=true", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=Bad&value=true", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=active&value=", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=active;value=true", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=active&value=true+false", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=active&value=%5B%5D", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=active&value=%7B%7D", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=active&index=active&value=true", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=missing&value=true", status: http.StatusNotFound, code: "index_not_found"},
		{path: "/missing?index=active&value=true", status: http.StatusNotFound, code: "database_not_found"},
		{path: "/users?index=active&value=" + url.QueryEscape(`"`+strings.Repeat("x", maxIndexValue)+`"`), status: http.StatusBadRequest, code: "invalid_query"},
	}
	for _, tt := range tests {
		res = sendRequest(t, server, http.MethodGet, tt.path, "", "")
		body := readResponse(t, res)
		if res.StatusCode != tt.status {
			t.Errorf("GET %s status = %d, want %d; body = %s", tt.path, res.StatusCode, tt.status, body)
		}
		if !strings.Contains(body, `"code":"`+tt.code+`"`) {
			t.Errorf("GET %s body = %s, want code %q", tt.path, body, tt.code)
		}
	}
}
