package config

import (
	"os"
)

type Config struct {
	Bind           string
	DatabaseURL    string
	LogLevel       string
	MigrateOnStart bool
}

func Load() Config {
	return Config{
		Bind:           envOr("TASKS127_BIND", "127.0.0.1:8080"),
		DatabaseURL:    envOr("TASKS127_DATABASE_URL", "sqlite://./tasks127.db"),
		LogLevel:       envOr("TASKS127_LOG_LEVEL", "info"),
		MigrateOnStart: envOr("TASKS127_MIGRATE_ON_START", "true") == "true",
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
