package config

import (
	"os"
	"strings"
)

type Config struct {
	Addr         string
	DBPath       string
	YTClientID   string
	YTSecret     string
	SPClientID   string
	SPSecret     string
	TLSCertFile  string
	TLSKeyFile   string
	OAuthBaseURL string
}

func Load() *Config {
	return &Config{
		Addr:         getEnv("ADDR", ":8080"),
		DBPath:       getEnv("DB_PATH", "routarr.db"),
		YTClientID:   strings.TrimSpace(os.Getenv("YT_CLIENT_ID")),
		YTSecret:     strings.TrimSpace(os.Getenv("YT_SECRET")),
		SPClientID:   strings.TrimSpace(os.Getenv("SP_CLIENT_ID")),
		SPSecret:     strings.TrimSpace(os.Getenv("SP_SECRET")),
		TLSCertFile:  os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:   os.Getenv("TLS_KEY_FILE"),
		OAuthBaseURL: getEnv("OAUTH_BASE_URL", "http://localhost:8080"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}
