package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Env                 string
	Addr                string
	DatabaseURL         string
	SessionSecret       []byte
	SessionCookieSecure bool
}

func Load() (Config, error) {
	cfg := Config{
		Env:         envOr("APP_ENV", "development"),
		Addr:        envOr("ADDR", ":8080"),
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
	}
	secret := os.Getenv("SESSION_SECRET")
	if len(secret) < 32 || strings.Contains(secret, "replace-with-a-random-secret") {
		return Config{}, errors.New("SESSION_SECRET wajib diatur dan minimal 32 karakter")
	}
	cfg.SessionSecret = []byte(secret)
	secure := strings.EqualFold(os.Getenv("SESSION_COOKIE_SECURE"), "true")
	cfg.SessionCookieSecure = secure
	if strings.EqualFold(cfg.Env, "production") && !secure {
		return Config{}, fmt.Errorf("SESSION_COOKIE_SECURE harus true saat APP_ENV=production")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL wajib diatur")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
