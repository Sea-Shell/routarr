package config

import (
	"os"
	"strings"
)

type Config struct {
	Addr       string
	DBPath     string
	YTClientID string
	YTSecret   string
	SPClientID string
	SPSecret   string
}

func Load() *Config {
	return &Config{
		Addr:       getEnv("ADDR", ":8080"),
		DBPath:     getEnv("DB_PATH", "yt2sp.db"),
		YTClientID: strings.TrimSpace(os.Getenv("YT_CLIENT_ID")),
		YTSecret:   strings.TrimSpace(os.Getenv("YT_SECRET")),
		SPClientID: strings.TrimSpace(os.Getenv("SP_CLIENT_ID")),
		SPSecret:   strings.TrimSpace(os.Getenv("SP_SECRET")),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}
