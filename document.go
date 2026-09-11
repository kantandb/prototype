package main

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const (
	maxStoredDocBytes = 64 << 20
	maxDocRecordBytes = maxStoredDocBytes + 1<<20
)

var (
	docEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
		return zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	})
	docDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
		return zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(maxStoredDocBytes),
			zstd.WithDecodeAllCapLimit(true),
		)
	})
)

func sealDoc(pebbleKey, databaseKey []byte, id string, rev revision, json []byte) ([]byte, error) {
	if len(json) > maxStoredDocBytes {
		return nil, errors.New("document exceeds storage limit")
	}
	key, err := deriveDocumentKey(databaseKey, id)
	if err != nil {
		return nil, fmt.Errorf("deriving document key: %w", err)
	}
	defer clear(key)

	encoder, err := docEncoder()
	if err != nil {
		return nil, fmt.Errorf("creating Zstandard encoder: %w", err)
	}
	compressed := encoder.EncodeAll(json, nil)
	if len(compressed) > maxDocRecordBytes-docRecordHeaderLen-gcmTagLen {
		return nil, errors.New("compressed document exceeds storage limit")
	}

	box, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	header := make([]byte, docRecordHeaderLen)
	header[0] = docRecordVersion
	header[1] = cipherSuite1
	copy(header[2:], rev[:])
	binary.BigEndian.PutUint64(header[2+len(rev):], uint64(len(json)))
	nonce := header[2+len(rev)+8:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating document nonce: %w", err)
	}

	return box.Seal(header, nonce, compressed, recordAD(pebbleKey, header)), nil
}

func openDoc(pebbleKey, databaseKey []byte, id string, value []byte) (storedDoc, error) {
	header, rev, length, err := parseDocRecord(value)
	if err != nil {
		return storedDoc{}, err
	}
	key, err := deriveDocumentKey(databaseKey, id)
	if err != nil {
		return storedDoc{}, fmt.Errorf("deriving document key: %w", err)
	}
	defer clear(key)

	box, err := newGCM(key)
	if err != nil {
		return storedDoc{}, err
	}
	nonce := header[2+len(rev)+8:]
	compressed, err := box.Open(nil, nonce, value[docRecordHeaderLen:], recordAD(pebbleKey, header))
	if err != nil {
		return storedDoc{}, errCorruptData
	}
	if length > maxStoredDocBytes {
		return storedDoc{}, errCorruptData
	}

	decoder, err := docDecoder()
	if err != nil {
		return storedDoc{}, fmt.Errorf("creating Zstandard decoder: %w", err)
	}
	json, err := decoder.DecodeAll(compressed, make([]byte, 0, int(length)))
	if err != nil || uint64(len(json)) != length {
		return storedDoc{}, errCorruptData
	}

	return storedDoc{json: json, revision: rev}, nil
}

func parseDocRecord(value []byte) ([]byte, revision, uint64, error) {
	if len(value) < docRecordHeaderLen+gcmTagLen || len(value) > maxDocRecordBytes {
		return nil, revision{}, 0, errCorruptData
	}
	if value[0] != docRecordVersion || value[1] != cipherSuite1 {
		return nil, revision{}, 0, errCorruptData
	}

	header := value[:docRecordHeaderLen]
	var rev revision
	copy(rev[:], header[2:2+len(rev)])
	length := binary.BigEndian.Uint64(header[2+len(rev) : 2+len(rev)+8])

	return header, rev, length, nil
}
