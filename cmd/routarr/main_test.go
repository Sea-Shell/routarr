package main

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bateau84/routarr/internal/adapters/sqlite"
	"github.com/bateau84/routarr/internal/adapters/web"
)

// TestRouteRegistration mirrors main() exactly.
// net/http.ServeMux panics at registration time when two patterns conflict, so
// any mux conflict introduced in either RegisterRoutes or main() will surface
// here as a test failure rather than a production startup crash.
func TestRouteRegistration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("close db: %v", closeErr)
		}
	}()

	mappingRepo := sqlite.NewMappingRepository(db)
	webHandler, err := web.NewHandler(db, mappingRepo, "http://localhost:8080", "test-yt-id", "test-yt-secret", "test-sp-id", "test-sp-secret", nil, nil)
	if err != nil {
		t.Fatalf("init web handler: %v", err)
	}

	// Mirrors the registration block in main(). Keep this in sync with main().
	mux := http.NewServeMux()
	webHandler.RegisterRoutes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
}
