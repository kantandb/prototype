package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
)

func BenchmarkDocumentCodec(b *testing.B) {
	databaseKey := bytes.Repeat([]byte{3}, keySize)
	key := docKey("db", "id")
	random := make([]byte, 1<<20)
	if _, err := rand.Read(random); err != nil {
		b.Fatalf("rand.Read() error = %v", err)
	}

	cases := []struct {
		name string
		body []byte
	}{
		{name: "small", body: []byte(`{"name":"KantanDB"}`)},
		{name: "repetitive-1MiB", body: bytes.Repeat([]byte("a"), 1<<20)},
		{name: "random-1MiB", body: random},
	}
	for _, test := range cases {
		b.Run(fmt.Sprintf("seal/%s", test.name), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(test.body)))
			for range b.N {
				if _, err := sealDoc(key, databaseKey, "id", revision{}, test.body); err != nil {
					b.Fatalf("sealDoc() error = %v", err)
				}
			}
		})

		record, err := sealDoc(key, databaseKey, "id", revision{}, test.body)
		if err != nil {
			b.Fatalf("sealDoc() setup error = %v", err)
		}
		b.Run(fmt.Sprintf("open/%s", test.name), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(test.body)))
			b.ReportMetric(float64(len(record))/float64(len(test.body)), "stored/raw")
			for range b.N {
				if _, err := openDoc(key, databaseKey, "id", record); err != nil {
					b.Fatalf("openDoc() error = %v", err)
				}
			}
		})
	}
}
