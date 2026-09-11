# Query language

Documents are queried with `GET /{database}` or `QUERY /{database}`. The response contains document IDs, not the document bodies:

```json
{
    "documents": ["01950000-0000-7000-8000-000000000001"],
    "cursor": ""
}
```

Fetch a returned document with `GET /{database}/{id}`.

## Query forms

There are three query forms:

```text
GET /users
    List every document ID in users.

GET /users?index=email&value=%22alice%40example.com%22
    Find IDs through the email secondary index.

QUERY /users
Content-Type: application/json

{"path":"$.profile.age","op":"ge","value":18}
    Find IDs through an RFC 9535 JSONPath.
```

An indexed query has one condition:

```text
index <operator> JSON scalar
```

The URL parameters are:

| Parameter | Meaning                                | Default |
| --------- | -------------------------------------- | ------- |
| `index`   | Secondary-index name                   | none    |
| `value`   | One JSON scalar to compare             | none    |
| `op`      | `eq`, `lt`, `le`, `gt`, or `ge`        | `eq`    |
| `limit`   | Page size from 1 to 1000               | 100     |
| `cursor`  | Position returned by the previous page | empty   |

`index` and `value` must appear together. `op` is only valid with them. Unknown or repeated parameters are rejected.

A QUERY request has no URI parameters. Its JSON body has these fields:

| Field    | Meaning                                | Default  |
| -------- | -------------------------------------- | -------- |
| `path`   | RFC 9535 JSONPath                      | required |
| `value`  | One JSON scalar to compare             | required |
| `op`     | `eq`, `lt`, `le`, `gt`, or `ge`        | `eq`     |
| `limit`  | Page size from 1 to 1000               | 100      |
| `cursor` | Position returned by the previous page | empty    |

The body must use `Content-Type: application/json`. Unknown or duplicate fields, trailing JSON, and URI parameters are rejected. QUERY responses use `Cache-Control: no-store`. Collection responses advertise the format with `Accept-Query: application/json`.

## Values and operators

`value` uses JSON syntax, then URL encoding. This distinction matters for strings:

```text
value=true                    JSON boolean
value=34                      JSON number
value=null                    JSON null
value=%22alice%40example.com%22
                              JSON string "alice@example.com"
```

With `curl`, `--data-urlencode` avoids manual encoding:

```sh
curl --get http://localhost:8080/users \
  --data index=email \
  --data-urlencode 'value="alice@example.com"'
```

Equality (`eq`) accepts strings, numbers, booleans, and `null`. Range operators work only with strings and numbers:

| Operator | Test                     |
| -------- | ------------------------ |
| `eq`     | equal to                 |
| `lt`     | less than                |
| `le`     | less than or equal to    |
| `gt`     | greater than             |
| `ge`     | greater than or equal to |

Numbers are compared exactly, without floating-point rounding. Equivalent JSON forms such as `1`, `1.0`, and `1e0` match each other. Strings use binary UTF-8 order. Types are not converted, so the string `"34"` does not match the number `34`.

A document is absent from an index when the indexed path is missing or contains an object or array. Such documents cannot match an indexed query.

JSONPath returns a list of nodes. A document matches a QUERY request when at least one selected scalar satisfies the comparison. Missing paths and selected objects or arrays do not match. Select their scalar children with JSONPath selectors such as `[*]` when needed.

## Examples

```text
GET /users?index=active&value=true
    Users whose indexed active field is true.

GET /users?index=age&op=ge&value=18
    Users whose indexed age is at least 18.

GET /users?index=name&op=lt&value=%22m%22
    Users whose indexed name sorts before "m".

GET /users?limit=25
    First 25 IDs without using a secondary index.

QUERY /users
{"path":"$.items[*].price","op":"lt","value":10,"limit":25}
    Users with any item priced below 10.
```

The language currently has no `AND`, `OR`, joins, projections, custom sorting, or full-text search.

## Ordering and pagination

```text
request with limit
        │
        ▼
 IDs plus one page cursor
        │
        ▼
same request with cursor
        │
        ▼
      next page
```

Unfiltered lists and equality queries are ordered by document ID. Range queries are ordered by indexed value, then document ID when values are equal.

A simple JSONPath is translated to JSON Pointer. If it matches a declared index, QUERY uses index order. Other JSONPaths scan in document ID order. A scan page examines at most 10,000 documents, so it can return an empty page with a continuation cursor. Each request runs for at most five seconds.

If more results exist, `cursor` is non-empty. Send it back with the same query. The final page returns an empty cursor. A cursor does not identify a fixed snapshot across requests, so writes between pages can change later results.

For an unfiltered list, the cursor is the last document ID. For an indexed query, it is an opaque, authenticated token tied to the database, index, operator, and value. Do not inspect or modify it. Indexed cursors are also tied to the running server process and become invalid after a restart.

QUERY cursors are opaque and authenticated. They bind the method, database, normalized path, operator, value, chosen order, and scan position. Send one only with the same request body, apart from `limit`. A cursor stays valid after restart but becomes invalid if the database is deleted and recreated.

QUERY bodies are limited to 64 KiB. Paths are limited to 256 bytes, 16 levels of bracket or parenthesis nesting, and 32 top-level segments. To bound evaluation, paths may contain at most one descendant segment and one selector per segment. A document may contain at most 1,000,000 JSON nodes for scan evaluation.
