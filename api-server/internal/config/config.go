package config

import "os"

// Config holds environment-variable configuration for the API server.
type Config struct {
	DatabaseURL       string
	EncryptionKey     string
	RestateIngressURL string
	RestateAdminURL   string
	ListenAddr        string
}

// Load reads configuration from environment variables.
func Load() Config {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8090"
	}
	return Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		EncryptionKey:     os.Getenv("ENCRYPTION_KEY"),
		RestateIngressURL: os.Getenv("RESTATE_INGRESS_URL"),
		RestateAdminURL:   os.Getenv("RESTATE_ADMIN_URL"),
		ListenAddr:        addr,
	}
}
