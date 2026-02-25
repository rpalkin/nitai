package config

import "os"

// Config holds environment-variable configuration for the worker.
type Config struct {
	DatabaseURL   string
	EncryptionKey string
	WorkerAddr    string
}

// Load reads configuration from environment variables.
func Load() Config {
	addr := os.Getenv("WORKER_ADDR")
	if addr == "" {
		addr = ":9080"
	}
	return Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		EncryptionKey: os.Getenv("ENCRYPTION_KEY"),
		WorkerAddr:    addr,
	}
}
