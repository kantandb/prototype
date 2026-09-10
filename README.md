# KantanDB prototype

Start the server:

```sh
mise run build
./kantan -addr :8080 -data data -max-body-bytes 1048576
```

## HTTP examples

```sh
# Check health.
xh GET localhost:8080/healthz

# Create a database with an email index.
xh POST localhost:8080/ name=example \
  indexes:='[{"name":"email","path":"/email"}]'

# List databases, optionally after a cursor.
xh GET localhost:8080/ limit==100 cursor==example

# Create a document.
xh POST localhost:8080/example/ name=KantanDB \
  email=alice@example.com active:=true

# List document IDs, optionally after a cursor.
xh GET localhost:8080/example limit==100 cursor==01950000-0000-7000-8000-000000000001

# Query the email index. value is a JSON string.
xh GET localhost:8080/example index==email value=='"alice@example.com"' limit==100

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
