package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	for _, body := range []string{`{"active":true}`, `{"active":false}`, `{"active":true}`, `{"active":true}`} {
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
	if page.Cursor == active[0] {
		t.Error("query cursor exposes the document ID")
	}

	cursor := url.QueryEscape(page.Cursor)
	res = sendRequest(t, server, http.MethodGet, "/users?index=active&value=false&cursor="+cursor, "", "")
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_cursor","message":"Cursor is invalid"}}`)

	path := "/users?index=active&value=true&limit=1&cursor=" + cursor
	res = sendRequest(t, server, http.MethodGet, path, "", "")
	page = docList{}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	if !slices.Equal(page.Documents, active[1:2]) {
		t.Errorf("middle documents = %v, want %v", page.Documents, active[1:2])
	}
	if page.Cursor == "" {
		t.Error("cursor is empty on middle page")
	}

	path = "/users?index=active&value=true&limit=1&cursor=" + url.QueryEscape(page.Cursor)
	res = sendRequest(t, server, http.MethodGet, path, "", "")
	page = docList{}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Errorf("Response.Body.Close() error = %v", err)
	}
	if !slices.Equal(page.Documents, active[2:]) {
		t.Errorf("final documents = %v, want %v", page.Documents, active[2:])
	}
	if page.Cursor != "" {
		t.Errorf("final cursor = %q, want empty", page.Cursor)
	}
}

func TestQueryScalarValuesHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	res := sendRequest(t, server, http.MethodPost, "/", `{"name":"values","indexes":[{"name":"value","path":"/value"}]}`, "application/json")
	checkResponse(t, res, http.StatusCreated, `{"name":"values","indexes":[{"name":"value","path":"/value"}]}`)

	ids := make(map[string]string)
	for name, body := range map[string]string{
		"escaped": `{"value":"a b&c"}`,
		"empty":   `{"value":""}`,
		"one":     `{"value":1}`,
		"decimal": `{"value":1.0}`,
		"boolean": `{"value":true}`,
		"string":  `{"value":"true"}`,
		"null":    `{"value":null}`,
		"missing": `{}`,
		"object":  `{"value":{"nested":true}}`,
		"array":   `{"value":[1]}`,
	} {
		ids[name] = createQueryDoc(t, server, "/values/", body)
	}

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "escaped string", raw: `"a b&c"`, want: []string{ids["escaped"]}},
		{name: "empty string", raw: `""`, want: []string{ids["empty"]}},
		{name: "normalized number", raw: `1e0`, want: []string{ids["one"], ids["decimal"]}},
		{name: "boolean", raw: `true`, want: []string{ids["boolean"]}},
		{name: "string type", raw: `"true"`, want: []string{ids["string"]}},
		{name: "null", raw: `null`, want: []string{ids["null"]}},
		{name: "missing and compound values skipped", raw: `"ignored"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/values?index=value&value=" + url.QueryEscape(tt.raw)
			res := sendRequest(t, server, http.MethodGet, path, "", "")

			var page docList
			if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if err := res.Body.Close(); err != nil {
				t.Errorf("Response.Body.Close() error = %v", err)
			}
			slices.Sort(tt.want)
			if !slices.Equal(page.Documents, tt.want) {
				t.Errorf("documents = %v, want %v", page.Documents, tt.want)
			}
			if page.Cursor != "" {
				t.Errorf("cursor = %q, want empty", page.Cursor)
			}
		})
	}

	path := "/values?index=value&value=" + url.QueryEscape(`"ignored"`)
	res = sendRequest(t, server, http.MethodGet, path, "", "")
	checkResponse(t, res, http.StatusOK, `{"documents":[],"cursor":""}`)
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
		{path: "/users?index=active&value=true&cursor=bad", status: http.StatusBadRequest, code: "invalid_cursor"},
		{path: "/users?index=active&index=active&value=true", status: http.StatusBadRequest, code: "invalid_query"},
		{path: "/users?index=active&value=true&value=false", status: http.StatusBadRequest, code: "invalid_query"},
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

func createQueryDoc(t *testing.T, server *httptest.Server, path, body string) string {
	t.Helper()

	res := sendRequest(t, server, http.MethodPost, path, body, "application/json")
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

	return created.ID
}
