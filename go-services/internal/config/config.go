package config

import (
	"os"
	"time"
)

// Config holds environment-variable configuration for the worker.
type Config struct {
	DatabaseURL     string
	EncryptionKey   string
	WorkerAddr      string
	DebounceTimeout time.Duration
}

// Load reads configuration from environment variables.
func Load() Config {
	addr := os.Getenv("WORKER_ADDR")
	if addr == "" {
		addr = ":9080"
	}

	debounce := 3 * time.Minute
	if v := os.Getenv("DEBOUNCE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			debounce = d
		}
	}

	return Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		EncryptionKey:   os.Getenv("ENCRYPTION_KEY"),
		WorkerAddr:      addr,
		DebounceTimeout: debounce,
	}
}
