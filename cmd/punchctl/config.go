package main

import (
	"io"
	"net/http"
	"os"
	"time"
)

type commandConfig struct {
	addr    string
	token   string
	timeout time.Duration
	out     io.Writer
	errOut  io.Writer
	client  *http.Client
}

func applyDefaults(cfg commandConfig) commandConfig {
	if cfg.out == nil {
		cfg.out = os.Stdout
	}
	if cfg.errOut == nil {
		cfg.errOut = os.Stderr
	}
	if cfg.client == nil {
		cfg.client = http.DefaultClient
	}
	cfg.addr = envOrDefault("PUNCH_API_ADDR", defaultAPIAddr)
	cfg.token = os.Getenv("PUNCH_API_TOKEN")
	cfg.timeout = 5 * time.Second
	return cfg
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
