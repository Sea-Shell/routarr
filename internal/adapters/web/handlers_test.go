package web

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bateau84/yt2sp/internal/adapters/sqlite"
	"github.com/bateau84/yt2sp/internal/domain"
)

func TestCreateMappingAndIndex(t *testing.T) {
	t.Parallel()

	db, handler, mux := newTestHandler(t)
	repo := sqlite.NewMappingRepository(db)

	resp := performRequest(t, mux, http.MethodPost, "/mappings", url.Values{
		"youtube_playlist_id":    {"yt-playlist-123"},
		"youtube_playlist_title": {"YT Playlist"},
		"spotify_playlist_id":    {"sp-playlist-456"},
		"spotify_playlist_title": {"SP Playlist"},
	}.Encode(), "application/x-www-form-urlencoded")

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /mappings status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	mappings, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("list mappings: %v", err)
	}
	if len(mappings) != 1 {
		t.Fatalf("mapping count = %d, want 1", len(mappings))
	}

	setProviderConnected(t, db, providerYouTube)
	setProviderConnected(t, db, providerSpotify)

	indexResp := performRequest(t, mux, http.MethodGet, "/", "", "")
	if indexResp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", indexResp.StatusCode, http.StatusOK)
	}

	body := readBody(t, indexResp)
	if !strings.Contains(body, "Connected") {
		t.Fatalf("index body missing connected status: %q", body)
	}
	if !strings.Contains(body, "YT Playlist") {
		t.Fatalf("index body missing created mapping title: %q", body)
	}

	_ = handler
}

func TestSyncDetailAndMatchReviewFlow(t *testing.T) {
	t.Parallel()

	db, _, mux := newTestHandler(t)
	repo := sqlite.NewMappingRepository(db)

	mapping := &domain.PlaylistMapping{
		YTPlaylistID:    "yt-p1",
		YTPlaylistTitle: "YouTube Mix",
		SPPlaylistID:    "sp-p1",
		SPPlaylistTitle: "Spotify Mix",
	}
	if err := repo.Save(t.Context(), mapping); err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	startedAt := time.Now().UTC().Add(-10 * time.Minute)
	res, err := db.ExecContext(t.Context(), `
INSERT INTO sync_runs(mapping_id, started_at, status)
VALUES(?, ?, ?)
`, mapping.ID, startedAt, "pending")
	if err != nil {
		t.Fatalf("insert sync run: %v", err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last run id: %v", err)
	}

	_, err = db.ExecContext(t.Context(), `
INSERT INTO track_matches(
	youtube_video_id,
	youtube_title,
	spotify_track_id,
	spotify_track_title,
	spotify_artist,
	confidence,
	decision,
	decision_source,
	created_at
)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
`, "yt-video-1", "Video 1", "sp-track-1", "Track 1", "Artist 1", 0.55, string(domain.MatchPending), "matcher", time.Now().UTC())
	if err != nil {
		t.Fatalf("insert track match: %v", err)
	}

	_, err = db.ExecContext(t.Context(), `
INSERT INTO sync_items(sync_run_id, youtube_video_id, selected_spotify_track_id, action)
VALUES(?, ?, ?, ?)
`, runID, "yt-video-1", "sp-track-1", "pending_review")
	if err != nil {
		t.Fatalf("insert sync item: %v", err)
	}

	detailResp := performRequest(t, mux, http.MethodGet, fmt.Sprintf("/runs/%d", runID), "", "")
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /runs/{id} status = %d, want %d", detailResp.StatusCode, http.StatusOK)
	}

	body := readBody(t, detailResp)
	if !strings.Contains(body, "Confirmed") {
		t.Fatalf("sync detail page missing Confirmed button: %q", body)
	}
	if !strings.Contains(body, "Video 1") {
		t.Fatalf("sync detail page missing sync item: %q", body)
	}

	reviewResp := performRequest(t, mux, http.MethodGet, fmt.Sprintf("/runs/%d/review", runID), "", "")
	if reviewResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /runs/{id}/review status = %d, want %d", reviewResp.StatusCode, http.StatusOK)
	}
	reviewBody := readBody(t, reviewResp)
	if !strings.Contains(reviewBody, "Approve") {
		t.Fatalf("review page missing Approve action: %q", reviewBody)
	}

	approveResp := performRequest(t, mux, http.MethodPost, fmt.Sprintf("/runs/%d/review/approve", runID), url.Values{
		"youtube_video_id": {"yt-video-1"},
	}.Encode(), "application/x-www-form-urlencoded")
	if approveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve status = %d, want %d", approveResp.StatusCode, http.StatusSeeOther)
	}

	var decision string
	if err := db.QueryRowContext(t.Context(), `SELECT decision FROM track_matches WHERE youtube_video_id = ?`, "yt-video-1").Scan(&decision); err != nil {
		t.Fatalf("query updated decision: %v", err)
	}
	if decision != string(domain.MatchApproved) {
		t.Fatalf("decision = %q, want %q", decision, domain.MatchApproved)
	}

	confirmResp := performRequest(t, mux, http.MethodPost, fmt.Sprintf("/runs/%d/confirm", runID), "", "")
	if confirmResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /runs/{id}/confirm status = %d, want %d", confirmResp.StatusCode, http.StatusSeeOther)
	}
}

func TestOAuthStubConnectFlow(t *testing.T) {
	t.Parallel()

	db, _, mux := newTestHandler(t)

	connectResp := performRequest(t, mux, http.MethodGet, "/oauth/youtube/connect", "", "")
	if connectResp.StatusCode != http.StatusFound {
		t.Fatalf("GET /oauth/youtube/connect status = %d, want %d", connectResp.StatusCode, http.StatusFound)
	}

	location := connectResp.Header.Get("Location")
	if !strings.Contains(location, "/oauth/youtube/callback") {
		t.Fatalf("connect redirect location = %q, want callback URL", location)
	}

	callbackReq := httptest.NewRequest(http.MethodGet, location, nil)
	callbackReq.Host = "localhost"
	callbackResp := httptest.NewRecorder()
	mux.ServeHTTP(callbackResp, callbackReq)

	if callbackResp.Code != http.StatusSeeOther {
		t.Fatalf("oauth callback status = %d, want %d", callbackResp.Code, http.StatusSeeOther)
	}

	var provider string
	if err := db.QueryRowContext(t.Context(), `SELECT provider FROM oauth_tokens WHERE provider = ?`, providerYouTube).Scan(&provider); err != nil {
		t.Fatalf("query stored provider token: %v", err)
	}
	if provider != providerYouTube {
		t.Fatalf("stored provider = %q, want %q", provider, providerYouTube)
	}
}

func newTestHandler(t *testing.T) (*sql.DB, *Handler, *http.ServeMux) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "yt2sp-web.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("close db: %v", closeErr)
		}
	})

	h, err := NewHandler(db, sqlite.NewMappingRepository(db))
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	return db, h, mux
}

func setProviderConnected(t *testing.T, db *sql.DB, provider string) {
	t.Helper()

	_, err := db.ExecContext(t.Context(), `
INSERT INTO oauth_tokens(provider, access_token, refresh_token, expiry, scopes, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
	access_token = excluded.access_token,
	updated_at = excluded.updated_at
`, provider, "test-token", "", time.Now().UTC().Add(24*time.Hour), "stub", time.Now().UTC())
	if err != nil {
		t.Fatalf("set provider connected: %v", err)
	}
}

func performRequest(t *testing.T, mux *http.ServeMux, method, target, body, contentType string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "localhost"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	return rec.Result()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	return string(b)
}
