package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("DB_PATH", "")
	t.Setenv("YT_CLIENT_ID", "")
	t.Setenv("YT_SECRET", "")
	t.Setenv("SP_CLIENT_ID", "")
	t.Setenv("SP_SECRET", "")

	cfg := Load()

	if cfg.Addr != ":8080" {
		t.Fatalf("expected default Addr %q, got %q", ":8080", cfg.Addr)
	}
	if cfg.DBPath != "yt2sp.db" {
		t.Fatalf("expected default DBPath %q, got %q", "yt2sp.db", cfg.DBPath)
	}
	if cfg.YTClientID != "" {
		t.Fatalf("expected empty YTClientID, got %q", cfg.YTClientID)
	}
	if cfg.YTSecret != "" {
		t.Fatalf("expected empty YTSecret, got %q", cfg.YTSecret)
	}
	if cfg.SPClientID != "" {
		t.Fatalf("expected empty SPClientID, got %q", cfg.SPClientID)
	}
	if cfg.SPSecret != "" {
		t.Fatalf("expected empty SPSecret, got %q", cfg.SPSecret)
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	t.Setenv("ADDR", ":9090")
	t.Setenv("DB_PATH", "custom.db")
	t.Setenv("YT_CLIENT_ID", "yt-id")
	t.Setenv("YT_SECRET", "yt-secret")
	t.Setenv("SP_CLIENT_ID", "sp-id")
	t.Setenv("SP_SECRET", "sp-secret")

	cfg := Load()

	if cfg.Addr != ":9090" {
		t.Fatalf("expected Addr %q, got %q", ":9090", cfg.Addr)
	}
	if cfg.DBPath != "custom.db" {
		t.Fatalf("expected DBPath %q, got %q", "custom.db", cfg.DBPath)
	}
	if cfg.YTClientID != "yt-id" {
		t.Fatalf("expected YTClientID %q, got %q", "yt-id", cfg.YTClientID)
	}
	if cfg.YTSecret != "yt-secret" {
		t.Fatalf("expected YTSecret %q, got %q", "yt-secret", cfg.YTSecret)
	}
	if cfg.SPClientID != "sp-id" {
		t.Fatalf("expected SPClientID %q, got %q", "sp-id", cfg.SPClientID)
	}
	if cfg.SPSecret != "sp-secret" {
		t.Fatalf("expected SPSecret %q, got %q", "sp-secret", cfg.SPSecret)
	}
}
