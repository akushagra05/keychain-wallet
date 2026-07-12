// Package config loads runtime configuration from the environment (12-factor).
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port              string // HTTP listen port
	DatabaseURL       string // postgres:// connection string
	DeductAmountMinor int64  // fixed per-order deduction, in paise (spec: ₹100 = 10000)
	LogLevel          string // debug|info|warn|error
}

// Load reads configuration, applying sensible defaults. DatabaseURL is required.
func Load() (*Config, error) {
	cfg := &Config{
		Port:              env("PORT", "8080"),
		DatabaseURL:       env("DATABASE_URL", ""),
		DeductAmountMinor: 10000, // ₹100
		LogLevel:          env("LOG_LEVEL", "info"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	if v := os.Getenv("DEDUCT_AMOUNT_MINOR"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("DEDUCT_AMOUNT_MINOR must be a positive integer, got %q", v)
		}
		cfg.DeductAmountMinor = n
	}

	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
