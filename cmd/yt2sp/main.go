package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/bateau84/yt2sp/internal/adapters/sqlite"
	"github.com/bateau84/yt2sp/internal/adapters/web"
	"github.com/bateau84/yt2sp/internal/config"
)

func main() {
	cfg := config.Load()
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Printf("close db: %v", closeErr)
		}
	}()

	mappingRepo := sqlite.NewMappingRepository(db)
	webHandler, err := web.NewHandler(db, mappingRepo)
	if err != nil {
		log.Fatalf("init web handler: %v", err)
	}

	mux := http.NewServeMux()
	webHandler.RegisterRoutes(mux)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	log.Printf("web ui enabled at http://localhost%s", cfg.Addr)
	log.Printf("oauth callback stubs: %s, %s", fmt.Sprintf("http://localhost%s/oauth/youtube/callback", cfg.Addr), fmt.Sprintf("http://localhost%s/oauth/spotify/callback", cfg.Addr))
	log.Printf("starting yt2sp on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
