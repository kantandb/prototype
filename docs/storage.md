# Storage model

KantanDB keeps all logical databases in one [Pebble](https://github.com/cockroachdb/pebble) key-value store. The `-data` option selects its directory (`data` by default). Pebble owns the files in that directory, including its write log, manifests, and sorted-table (`.sst`) files. They should not be edited directly.

A KantanDB "database" is therefore a named collection inside one Pebble store, not a separate Pebble database or directory.

```text
-data directory
└── one Pebble store
    ├── database records
    ├── document records (the primary index)
    ├── secondary-index definitions
    └── secondary-index entries
```

## Logical key layout

The first byte of every key says what kind of record follows. Names and IDs are included in the key, so Pebble's byte ordering groups related records together.

```text
0x01 | database
    Database record

0x02 | database | 0x00 | document ID
    Document record / primary index entry

0x03 | len(database) | database | len(index) | index
    Secondary-index definition

0x04 | len(database) | database | len(index) | index | encoded value | document ID
    Secondary-index entry
```

Lengths are unsigned variable-length integers. They keep adjacent names unambiguous. The database record contains only a format-version byte.

## Documents and the primary index

A document's key contains its database name and ID. Its value is:

```text
format version (1 byte) | revision (16 bytes) | canonical JSON object
```

This key space is the primary index. There is no separate ID-to-document lookup table: reading an ID fetches the document record directly. Scanning the prefix `0x02 | database | 0x00` lists that database's IDs in byte order. IDs are UUIDv7 strings, so this order normally follows creation time.

The random 16-byte revision is exposed as the document ETag and changes when the document changes.

## Secondary indexes

A secondary index is declared when its database is created. Its definition stores the index name and a JSON Pointer such as `/email` or `/items/0/sku`.

For each document, KantanDB follows that pointer and writes an empty Pebble value under a key containing:

```text
database → index name → encoded field value → document ID
```

The document ID at the end handles duplicate values and points back to the primary record. For example, two users with the same indexed email produce two adjacent keys:

```text
users → email → "a@example.com" → 0195...001
users → email → "a@example.com" → 0195...002
```

Index values have type tags. `null`, booleans, numbers, and strings are supported. Numbers and strings use order-preserving encodings, which lets Pebble prefix and range scans answer equality and range queries. Missing fields, objects, and arrays do not get an index entry. Types are kept separate and are not coerced.

A query scans the matching index-key range, extracts each document ID, and checks that its primary record exists. Equal values are ordered by document ID; range results are ordered by encoded value and then document ID.

## How range queries work

Numbers and strings are encoded so their byte order matches their value order. All values of the same type also share a key prefix:

```text
index prefix → type → encoded value → document ID
               └──────── scan range ─────────┘
```

KantanDB turns an operator into Pebble iterator bounds around the requested value:

| Operator | Scanned keys                            |
| -------- | --------------------------------------- |
| `lt`     | Before the value                        |
| `le`     | Before or starting with the value       |
| `gt`     | After every key starting with the value |
| `ge`     | Starting with or after the value        |

The type prefix limits the scan to either numbers or strings. A numeric range cannot include strings, and a string range cannot include numbers. Booleans and `null` support equality only.

Each range query uses a Pebble snapshot, so its page sees a consistent view. The iterator moves forward in encoded-value order and reads one extra valid entry to determine whether another page exists. A continuation cursor records the last encoded value and document ID; together they identify the next scan position.

## Writes and deletion

Document creation, replacement, patching, and deletion update the primary record and all affected secondary entries in one synced Pebble batch. Readers cannot see a document change without its matching index change.

Deleting a logical database removes its database record and the ranges containing its documents, index definitions, and index entries in one batch.
