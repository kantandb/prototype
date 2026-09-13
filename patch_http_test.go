package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestPatchDocumentHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, defaultMaxBodyBytes)
	id, firstETag := createHTTPDoc(t, server, `{"name":"first","nested":{"a":1},"remove":true}`)
	path := "/db/dbname/" + id

	merge := `{"name":"second","nested":{"b":2},"remove":null}`
	res := sendMatchRequest(t, server, http.MethodPatch, path, merge, mergePatchType, firstETag)
	secondETag := res.Header.Get("ETag")
	if secondETag == firstETag {
		t.Error("merge patch did not change ETag")
	}
	checkResponse(t, res, http.StatusOK, `{"name":"second","nested":{"a":1,"b":2}}`)

	patch := `[{"op":"replace","path":"/name","value":"third"},{"op":"add","path":"/items","value":[1,2]}]`
	res = sendMatchRequest(t, server, http.MethodPatch, path, patch, jsonPatchType, "*")
	thirdETag := res.Header.Get("ETag")
	if thirdETag == secondETag {
		t.Error("JSON patch did not change ETag")
	}
	checkResponse(t, res, http.StatusOK, `{"items":[1,2],"name":"third","nested":{"a":1,"b":2}}`)

	res = sendMatchRequest(t, server, http.MethodPatch, path, `{}`, mergePatchType, firstETag)
	checkResponse(t, res, http.StatusPreconditionFailed, `{"error":{"code":"precondition_failed","message":"If-Match precondition failed"}}`)

	invalid := `[{"op":"remove","path":"/missing"}]`
	res = sendMatchRequest(t, server, http.MethodPatch, path, invalid, jsonPatchType, thirdETag)
	checkResponse(t, res, http.StatusBadRequest, `{"error":{"code":"invalid_patch","message":"Patch is invalid or produces a non-object document"}}`)

	res = sendRequest(t, server, http.MethodGet, path, "", "")
	if got := res.Header.Get("ETag"); got != thirdETag {
		t.Errorf("ETag after failed patch = %q, want %q", got, thirdETag)
	}
	checkResponse(t, res, http.StatusOK, `{"items":[1,2],"name":"third","nested":{"a":1,"b":2}}`)
}

func TestPatchDocumentValidationHTTP(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, 50)
	id, _ := createHTTPDoc(t, server, `{"first":"12345678901234567890"}`)
	path := "/db/dbname/" + id

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
		{name: "wrong content type", path: path, body: `{}`, contentType: "application/json", status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "malformed patch", path: path, body: `{"open":`, contentType: mergePatchType, status: http.StatusBadRequest, code: "invalid_patch"},
		{name: "non-object result", path: path, body: `null`, contentType: mergePatchType, status: http.StatusBadRequest, code: "invalid_patch"},
		{name: "body too large", path: path, body: strings.Repeat("x", 51), contentType: mergePatchType, status: http.StatusRequestEntityTooLarge, code: "content_too_large"},
		{name: "result too large", path: path, body: `{"second":"12345678901234567890"}`, contentType: mergePatchType, status: http.StatusRequestEntityTooLarge, code: "content_too_large"},
		{name: "malformed If-Match", path: path, body: `{}`, contentType: mergePatchType, ifMatch: "bad", status: http.StatusBadRequest, code: "invalid_if_match"},
		{name: "missing document", path: "/db/dbname/01950000-0000-7000-8000-000000000001", body: `{}`, contentType: mergePatchType, status: http.StatusNotFound, code: "document_not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := sendMatchRequest(t, server, http.MethodPatch, tt.path, tt.body, tt.contentType, tt.ifMatch)
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
