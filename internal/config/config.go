package config

import "os"

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
		YTClientID: os.Getenv("YT_CLIENT_ID"),
		YTSecret:   os.Getenv("YT_SECRET"),
		SPClientID: os.Getenv("SP_CLIENT_ID"),
		SPSecret:   os.Getenv("SP_SECRET"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}
