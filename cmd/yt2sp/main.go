package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/bateau84/yt2sp/internal/adapters/sqlite"
	"github.com/bateau84/yt2sp/internal/adapters/web"
	"github.com/bateau84/yt2sp/internal/config"
	"github.com/bateau84/yt2sp/internal/tlsutil"
)

func main() {
	cfg := config.Load()

	// Determine TLS mode and ensure cert/key files are present.
	certFile := cfg.TLSCertFile
	keyFile := cfg.TLSKeyFile
	tlsMode := false

	switch {
	case certFile == "" && keyFile == "":
		// No TLS vars set — serve HTTP as normal.
	case certFile == "" || keyFile == "":
		log.Fatal("TLS misconfiguration: both TLS_CERT_FILE and TLS_KEY_FILE must be set, or neither")
	default:
		// Both paths provided. Auto-generate only when files are absent; fail
		// fast on any other stat error (e.g. permission denied) rather than
		// surfacing a cryptic TLS listener error later.
		_, certErr := os.Stat(certFile)
		_, keyErr := os.Stat(keyFile)
		certMissing := os.IsNotExist(certErr)
		keyMissing := os.IsNotExist(keyErr)
		if certErr != nil && !certMissing {
			log.Fatalf("stat TLS cert file %s: %v", certFile, certErr)
		}
		if keyErr != nil && !keyMissing {
			log.Fatalf("stat TLS key file %s: %v", keyFile, keyErr)
		}
		if certMissing || keyMissing {
			log.Printf("TLS cert/key not found (%s, %s) — generating self-signed certificate", certFile, keyFile)
			if err := tlsutil.GenerateSelfSignedCert(certFile, keyFile); err != nil {
				log.Fatalf("generate self-signed cert: %v", err)
			}
			log.Printf("self-signed certificate written to %s and %s", certFile, keyFile)
		}
		tlsMode = true
	}

	// If HTTPS mode and the user did NOT explicitly set OAUTH_BASE_URL, derive
	// an https:// default from the listen address so OAuth callbacks match.
	if tlsMode && os.Getenv("OAUTH_BASE_URL") == "" {
		port := cfg.Addr
		if !strings.HasPrefix(port, ":") {
			// addr is "host:port" — keep only the ":port" suffix.
			if idx := strings.LastIndex(port, ":"); idx >= 0 {
				port = port[idx:]
			}
		}
		cfg.OAuthBaseURL = "https://localhost" + port
	}

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
	webHandler, err := web.NewHandler(db, mappingRepo, cfg.OAuthBaseURL, cfg.YTClientID, cfg.YTSecret, cfg.SPClientID, cfg.SPSecret, nil, nil)
	if err != nil {
		log.Fatalf("init web handler: %v", err)
	}

	mux := http.NewServeMux()
	webHandler.RegisterRoutes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	scheme := "http"
	if tlsMode {
		scheme = "https"
	}
	log.Printf("web ui enabled at %s://localhost%s", scheme, cfg.Addr)
	log.Printf("db path: %s", cfg.DBPath)
	log.Printf("oauth callbacks: %s/oauth/youtube/callback, %s/oauth/spotify/callback", cfg.OAuthBaseURL, cfg.OAuthBaseURL)
	log.Printf("starting yt2sp on %s (tls: %v)", cfg.Addr, tlsMode)

	if tlsMode {
		if err := http.ListenAndServeTLS(cfg.Addr, certFile, keyFile, mux); err != nil {
			log.Fatalf("server failed: %v", err)
		}
	} else {
		if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
			log.Fatalf("server failed: %v", err)
		}
	}
}
