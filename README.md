# KantanDB prototype

Start the server:

```sh
mise run build
./kantan -addr :8080 -data data -max-body-bytes 1048576
```

Check health at `GET /healthz`.
