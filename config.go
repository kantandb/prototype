package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const defaultMaxBodyBytes int64 = 1 << 20

type config struct {
	addr         string
	dataPath     string
	keyFile      string
	maxBodyBytes int64
}

func parseConfig(args []string) (config, error) {
	cfg := config{}
	flags := flag.NewFlagSet("kantan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.addr, "addr", ":8080", "HTTP listen address")
	flags.StringVar(&cfg.dataPath, "data", "data", "Pebble data directory")
	flags.StringVar(&cfg.keyFile, "key-file", "", "master-key file")
	flags.Int64Var(&cfg.maxBodyBytes, "max-body-bytes", defaultMaxBodyBytes, "maximum request body size")

	if err := flags.Parse(args); err != nil {
		return config{}, fmt.Errorf("parsing configuration: %w", err)
	}
	if cfg.addr == "" {
		return config{}, fmt.Errorf("validating configuration: address is empty")
	}
	if cfg.dataPath == "" {
		return config{}, fmt.Errorf("validating configuration: data path is empty")
	}
	if cfg.keyFile == "" {
		return config{}, fmt.Errorf("validating configuration: key file is empty")
	}
	if cfg.maxBodyBytes <= 0 {
		return config{}, fmt.Errorf("validating configuration: max body bytes must be positive")
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("validating configuration: unexpected arguments")
	}

	return cfg, nil
}

func loadMasterKey(path string) ([]byte, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key file: %w", err)
	}

	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("decoding key file: invalid base64")
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("decoding key file: key must be %d bytes", keySize)
	}

	return key, nil
}
