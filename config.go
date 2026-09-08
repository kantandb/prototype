package main

import (
	"flag"
	"fmt"
	"io"
)

const defaultMaxBodyBytes int64 = 1 << 20

type config struct {
	addr         string
	dataPath     string
	maxBodyBytes int64
}

func parseConfig(args []string) (config, error) {
	cfg := config{}
	flags := flag.NewFlagSet("kantan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.addr, "addr", ":8080", "HTTP listen address")
	flags.StringVar(&cfg.dataPath, "data", "data", "Pebble data directory")
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
	if cfg.maxBodyBytes <= 0 {
		return config{}, fmt.Errorf("validating configuration: max body bytes must be positive")
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("validating configuration: unexpected arguments")
	}

	return cfg, nil
}
