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

# Create a database.
xh POST localhost:8080/ name=example

# List databases.
xh GET localhost:8080/

# Create a document.
xh POST localhost:8080/example/ name=KantanDB active:=true

# Read a document using the returned ID.
xh GET localhost:8080/example/01950000-0000-7000-8000-000000000001

# Delete a document.
xh DELETE localhost:8080/example/01950000-0000-7000-8000-000000000001

# Delete a database.
xh DELETE localhost:8080/example
```
