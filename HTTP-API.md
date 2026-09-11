# HTTP API

This file describes the API exposed by the current prototype. Examples use
`http://localhost:8080` as the base URL.

## General behavior

- Request and response bodies are JSON unless stated otherwise.
- JSON request content types may include parameters such as
  `application/json; charset=utf-8`.
- Database and unfiltered document list order is lexical.
- Routes are strict about trailing slashes. In particular, document creation is
  `POST /{database}/`, while database operations use `/{database}`.
- Successful document writes store a canonical JSON object. Whitespace and
  object-key order from the request are not preserved.
- The configured maximum body size applies to database requests, documents,
  and patches. The default is 1 MiB when the server is started normally.

Database names contain 1 to 63 characters. The first character must be a
lowercase ASCII letter. Remaining characters may be lowercase ASCII letters,
digits, `_`, or `-`.

Document IDs are lowercase UUIDv7 strings:

```text
01950000-0000-7000-8000-000000000001
```

## Errors

Errors use one envelope:

```json
{
  "error": {
    "code": "database_not_found",
    "message": "Database does not exist"
  }
}
```

Common status and code pairs:

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid_request` | The database request body is invalid. |
| 400 | `invalid_name` | A database name is invalid. |
| 400 | `invalid_id` | A document ID is invalid. |
| 400 | `invalid_limit` | `limit` is missing a value, is not an integer, or is outside 1-1000. |
| 400 | `invalid_cursor` | A list cursor has the wrong format or belongs to another query. |
| 400 | `invalid_query` | A document query is malformed or ambiguous. |
| 400 | `invalid_document` | A document is invalid or has an oversized indexed value. |
| 400 | `invalid_patch` | A patch is invalid or its result is not a JSON object. |
| 400 | `invalid_if_match` | `If-Match` is malformed. |
| 404 | `database_not_found` | The requested database does not exist. |
| 404 | `document_not_found` | The requested document does not exist. |
| 404 | `index_not_found` | The requested index does not exist. |
| 409 | `database_exists` | A database with that name already exists. |
| 412 | `precondition_failed` | `If-Match` does not match the current revision. |
| 413 | `content_too_large` | A request body or resulting document exceeds the configured limit. |
| 415 | `unsupported_media_type` | The request has a missing or unsupported `Content-Type`. |
| 404 | `route_not_found` | No route matches the path. |
| 405 | `method_not_allowed` | The path exists but does not support the method. |
| 500 | `corrupt_data` | Stored data is corrupt. |
| 500 | `internal_error` | An unexpected server error occurred. |
| 503 | `service_unavailable` | Storage or the server is unavailable. |

## Health check

### `GET /healthz`

Returns `200 OK`:

```json
{"status":"ok"}
```

## Databases

### `POST /`

Creates a database.

Headers:

```http
Content-Type: application/json
```

Body:

```json
{
  "name": "example",
  "indexes": [
    {"name": "email", "path": "/email"},
    {"name": "active", "path": "/active"}
  ]
}
```

`indexes` is optional and can only be set when the database is created. When
present, it must be an array; `null` is invalid. An index name follows the
database-name rules. Its `path` is a non-empty JSON Pointer of at most 256
bytes. A database can have at most 16 indexes.

Returns `201 Created`:

```http
Location: /example
```

```json
{
  "name": "example",
  "indexes": [
    {"name": "email", "path": "/email"},
    {"name": "active", "path": "/active"}
  ]
}
```

When `indexes` is omitted, the response remains `{"name":"example"}`. An
explicit empty array remains `"indexes":[]` in the response.

Unknown fields, trailing JSON values, duplicate index names, invalid paths,
and non-object bodies return `400 invalid_request`. An invalid database name
returns `400 invalid_name`. Creating the same database twice returns
`409 database_exists`.

### `GET /`

Lists database names.

Query parameters:

| Parameter | Required | Description |
| --- | --- | --- |
| `limit` | No | Page size from 1 to 1000. Default: 100. |
| `cursor` | No | Valid database name. Results start strictly after it. |

The cursor does not need to name an existing database. It is a lexical position.
An empty cursor is treated as omitted. For the next page, use the last name from
the current page as `cursor`.

Example:

```http
GET /?limit=2&cursor=alpha
```

Returns `200 OK`:

```json
{"databases":["beta","gamma"]}
```

An empty page is represented as an array, not `null`:

```json
{"databases":[]}
```

### `DELETE /{database}`

Deletes a database and all documents in it.

Returns `204 No Content` with an empty body. A missing database returns
`404 database_not_found`.

## Documents

### `POST /{database}/`

Creates a document. The trailing slash is required.

Headers:

```http
Content-Type: application/json
```

The body must be one JSON object. Arrays, scalar values, malformed JSON,
multiple JSON values, and invalid UTF-8 return `400 invalid_document`.

Example body:

```json
{"name":"KantanDB","active":true}
```

Returns `201 Created`:

```http
Location: /example/01950000-0000-7000-8000-000000000001
ETag: "0123456789abcdef0123456789abcdef"
```

```json
{"id":"01950000-0000-7000-8000-000000000001"}
```

The ID and ETag are generated by the server. A missing database returns
`404 database_not_found`.

### `GET /{database}`

Lists document IDs, with an optional indexed comparison query.

Query parameters:

| Parameter | Required | Description |
| --- | --- | --- |
| `limit` | No | Page size from 1 to 1000. Default: 100. |
| `cursor` | No | Cursor returned by the previous page. |
| `index` | With `value` | Index name. |
| `op` | No | `eq`, `lt`, `le`, `gt`, or `ge`. Default: `eq`. Requires `index` and `value`. |
| `value` | With `index` | One URL-encoded JSON scalar. |

Each parameter may appear at most once. Unknown and duplicate parameters return
`400 invalid_query`.

Without `index` and `value`, the endpoint lists every document in lexical ID
order. Its request cursor is a valid document ID, and results start strictly
after it. The ID does not need to identify an existing document.

```http
GET /example?limit=2&cursor=01950000-0000-7000-8000-000000000001
```

```json
{
  "documents": [
    "01950000-0000-7000-8000-000000000002",
    "01950000-0000-7000-8000-000000000003"
  ],
  "cursor": "01950000-0000-7000-8000-000000000003"
}
```

For an indexed query, `value` must decode to `null`, a Boolean, a number, or a
string. Arrays, objects, malformed JSON, and trailing JSON return
`400 invalid_query`. Omitting `op` selects equality. The ordering operators
only accept numbers and strings; using them with `null` or a Boolean returns
`400 invalid_query`.

```http
GET /example?index=email&value=%22alice%40example.com%22&limit=50
GET /example?index=active&op=eq&value=true
GET /example?index=score&op=ge&value=1.5
GET /example?index=deleted_at&value=null
```

A range query returns matching IDs in `(indexed value, document ID)` order.
Equality remains in document ID order because all matched values are equal:

```json
{
  "documents": ["01950000-0000-7000-8000-000000000001"],
  "cursor": "AQVlbWFpbA..."
}
```

Indexed cursors are opaque and versioned. A cursor only works with the same
database, index, operator, and equivalent value. Restarting the server
invalidates issued indexed cursors. A malformed or mismatched cursor returns
`400 invalid_cursor`.

Every successful document-list response includes both fields. `documents` is
never `null`. `cursor` is a string and is empty when there is no next page:

```json
{"documents":[],"cursor":""}
```

Index comparisons use these rules:

- Missing paths and object or array values do not match.
- `null` differs from a missing path and can be queried with `eq`.
- Boolean, number, and string values have distinct types. Types are not coerced.
- Numbers compare exactly, so `1`, `1.0`, and `1e0` are equivalent.
- Strings use binary UTF-8 order.
- An indexed scalar may use at most 4 KiB in the index encoding. A larger value
  causes the document write to fail.

Indexed queries use bounded index scans and read at most `limit + 1` valid
entries. Range-scan cost grows with the matching portion of the index, not the
number of documents.

For example, request the first page of an age comparison:

```http
GET /example?index=age&op=ge&value=30&limit=2
```

```json
{
  "documents": [
    "01950000-0000-7000-8000-000000000001",
    "01950000-0000-7000-8000-000000000004"
  ],
  "cursor": "AgdleGFtcGxlA2FnZQQDMzA..."
}
```

Pass the opaque cursor with the same comparison to read the next page:

```http
GET /example?index=age&op=ge&value=30&limit=2&cursor=AgdleGFtcGxlA2FnZQQDMzA...
```

Pagination resumes after the last `(indexed value, document ID)` pair and is
not snapshot-isolated across requests. Concurrent writes can change later
pages. A missing database returns
`404 database_not_found`; a missing index returns `404 index_not_found`.

### `GET /{database}/{id}`

Returns the stored document and its current ETag.

```http
HTTP/1.1 200 OK
Content-Type: application/json
ETag: "0123456789abcdef0123456789abcdef"
```

```json
{"active":true,"name":"KantanDB"}
```

A missing document returns `404 document_not_found`. This endpoint also returns
`document_not_found` when the database itself is missing.

### `PUT /{database}/{id}`

Replaces the whole document. It does not create a missing document.

Headers:

```http
Content-Type: application/json
If-Match: "0123456789abcdef0123456789abcdef"
```

`If-Match` is optional. See [Conditional writes](#conditional-writes).

The body follows the same rules as document creation. On success, the endpoint
returns `200 OK`, the replacement document, and a new ETag:

```http
ETag: "fedcba9876543210fedcba9876543210"
```

```json
{"name":"Replacement"}
```

### `PATCH /{database}/{id}`

Patches an existing document. Two patch formats are accepted:

```http
Content-Type: application/merge-patch+json
```

```json
{"active":false,"obsolete":null}
```

or:

```http
Content-Type: application/json-patch+json
```

```json
[
  {"op":"replace","path":"/name","value":"Patched"},
  {"op":"add","path":"/items","value":[1,2]}
]
```

Content-Type parameters are accepted. Negative JSON Patch array indices are
not supported.

On success, the endpoint returns `200 OK`, the patched document, and a new
ETag. A patch that fails or produces anything other than a JSON object returns
`400 invalid_patch` without changing the document. If the resulting document
is too large, the response is `413 content_too_large`.

### `DELETE /{database}/{id}`

Deletes an existing document. `If-Match` is optional.

A successful deletion returns `204 No Content` with an empty body. A missing
document returns `404 document_not_found`.

## Conditional writes

`PUT`, `PATCH`, and document `DELETE` accept one optional `If-Match` header.
There are three forms:

| Header | Behavior |
| --- | --- |
| omitted | Apply without a revision check. |
| `If-Match: *` | Apply if the document exists. |
| `If-Match: "<32 lowercase hex characters>"` | Apply only if the ETag matches. |

Weak ETags, unquoted values, comma-separated lists, uppercase hex, and multiple
`If-Match` headers return `400 invalid_if_match`.

A stale ETag returns:

```http
HTTP/1.1 412 Precondition Failed
Content-Type: application/json
```

```json
{
  "error": {
    "code": "precondition_failed",
    "message": "If-Match precondition failed"
  }
}
```

Every successful create, replacement, or patch returns the current ETag.
Replacement and patch generate a different ETag. A failed conditional write
leaves both the document and its ETag unchanged.

## Suggested lifecycle test

A cross-repository lifecycle test can follow this sequence:

1. Check `GET /healthz`.
2. Create a uniquely named database with `POST /` and capture `Location`.
3. Page through `GET /` until the new database appears.
4. Create two documents with `POST /{database}/`; capture each `id`, `Location`,
   and `ETag`.
5. Page through `GET /{database}?limit=1`, using the returned `cursor`.
6. Read one document and compare its ETag with the create response.
7. Replace it with `PUT` and the captured ETag; capture the new ETag.
8. Assert that a write using the stale ETag returns `412`.
9. Apply merge and JSON patches, checking the body and changed ETag each time.
10. Delete both documents, then check that the document list is empty.
11. Delete the database and confirm `GET /{database}` returns `404`.
