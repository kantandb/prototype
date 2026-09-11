# KantanDB prototype

Create a 256-bit master key, keep it outside the data directory, then start the server:

```sh
openssl rand -base64 32 > kantan.key
chmod 600 kantan.key
mise run build
./kantan -addr :8080 -data data -key-file kantan.key -max-body-bytes 1048576
```

`-key-file` is required. Its file must contain one base64-encoded 32-byte key. Losing the key makes the data unreadable. Using the wrong key or opening an older plaintext store fails at startup.

## HTTP examples

```sh
# Check health.
xh GET localhost:8080/healthz

# Create a database with email and age indexes.
xh POST localhost:8080/ name=example \
  indexes:='[{"name":"email","path":"/email"},{"name":"age","path":"/age"}]'

# List databases, optionally after a cursor.
xh GET localhost:8080/ limit==100 cursor==example

# Create a document.
xh POST localhost:8080/example/ name=KantanDB \
  email=alice@example.com age:=34 active:=true

# List document IDs, optionally after a cursor.
xh GET localhost:8080/example limit==100 cursor==01950000-0000-7000-8000-000000000001

# Query the email index. value is a JSON string; op defaults to eq.
xh GET localhost:8080/example index==email value=='"alice@example.com"' limit==100

# Find documents whose indexed age is at least 30.
xh GET localhost:8080/example index==age op==ge value==30 limit==100

# Query any JSONPath without putting it in the URI.
echo '{"path":"$.age","op":"ge","value":30,"limit":100}' | \
  xh QUERY localhost:8080/example Content-Type:application/json

# Continue a JSONPath query with its opaque cursor.
echo '{"path":"$.age","op":"ge","value":30,"cursor":"eyJ..."}' | \
  xh QUERY localhost:8080/example Content-Type:application/json

# Continue an index query with its opaque cursor.
xh GET localhost:8080/example index==age op==ge value==30 cursor==eyJ...

# Read a document using the returned ID.
xh GET localhost:8080/example/01950000-0000-7000-8000-000000000001

# Replace a document if its ETag still matches.
xh PUT localhost:8080/example/01950000-0000-7000-8000-000000000001 \
  If-Match:'"0123456789abcdef0123456789abcdef"' name=Updated

# Merge fields into a document.
xh PATCH localhost:8080/example/01950000-0000-7000-8000-000000000001 \
  Content-Type:application/merge-patch+json active:=false

# Apply a JSON Patch.
echo '[{"op":"replace","path":"/name","value":"Patched"}]' | \
  xh PATCH localhost:8080/example/01950000-0000-7000-8000-000000000001 \
  Content-Type:application/json-patch+json

# Delete a document if it exists.
xh DELETE localhost:8080/example/01950000-0000-7000-8000-000000000001 If-Match:\*

# Delete a database.
xh DELETE localhost:8080/example
```

Index queries support `eq`, `lt`, `le`, `gt`, and `ge`. If `op` is omitted,
`eq` is used. Equality accepts any JSON scalar. Ordering accepts numbers and
strings. Types are not coerced; missing paths, objects, arrays, and values of a
different type do not match. Number comparisons are exact. Strings use binary
UTF-8 order.

Index queries read at most one page plus one extra valid index entry. Range
results use indexed-value order, with document ID as the tie-breaker. Equality
results remain in document ID order. Reuse the returned cursor with the same
database, index, operator, and value.

`QUERY /{database}` accepts RFC 9535 JSONPath in a JSON body. A document matches
when any selected scalar satisfies the comparison. A simple path that matches a
declared index uses that index; other paths scan documents in ID order. Each
scan page examines at most 10,000 documents and runs for at most five seconds.
The request body is limited to 64 KiB and the path to 256 bytes.
