package web

import (
	"context"
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
	"golang.org/x/oauth2"
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

func TestDeleteMapping(t *testing.T) {
	t.Parallel()

	db, _, mux := newTestHandler(t)
	repo := sqlite.NewMappingRepository(db)

	mapping := &domain.PlaylistMapping{
		YTPlaylistID:    "yt-delete-1",
		YTPlaylistTitle: "YT Delete",
		SPPlaylistID:    "sp-delete-1",
		SPPlaylistTitle: "SP Delete",
	}
	if err := repo.Save(t.Context(), mapping); err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	resp := performRequest(t, mux, http.MethodPost, fmt.Sprintf("/mappings/%d/delete", mapping.ID), "", "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /mappings/{id}/delete status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	remaining, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("list mappings: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("mapping count = %d, want 0", len(remaining))
	}
}

func TestSyncDetailAndMatchReviewFlow(t *testing.T) {
	t.Parallel()

	var runID int64
	committer := &syncCommitterStub{}
	db, _, mux := newTestHandler(t, committer)
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
	runID, err = res.LastInsertId()
	if err != nil {
		t.Fatalf("last run id: %v", err)
	}
	committer.commitFn = func(ctx context.Context, gotRunID int) error {
		if int64(gotRunID) != runID {
			return fmt.Errorf("commit run id = %d, want %d", gotRunID, runID)
		}

		if _, err := db.ExecContext(ctx, `
UPDATE sync_items
SET action = ?, error = NULL
WHERE sync_run_id = ? AND youtube_video_id = ?
`, "added", runID, "yt-video-1"); err != nil {
			return fmt.Errorf("update sync item from commit stub: %w", err)
		}

		if _, err := db.ExecContext(ctx, `
UPDATE sync_runs
SET status = ?, finished_at = ?
WHERE id = ?
`, "completed", time.Now().UTC(), runID); err != nil {
			return fmt.Errorf("update sync run from commit stub: %w", err)
		}

		return nil
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

	setProviderConnected(t, db, providerSpotify)

	confirmResp := performRequest(t, mux, http.MethodPost, fmt.Sprintf("/runs/%d/confirm", runID), "", "")
	if confirmResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /runs/{id}/confirm status = %d, want %d", confirmResp.StatusCode, http.StatusSeeOther)
	}

	var runStatus string
	if err := db.QueryRowContext(t.Context(), `SELECT status FROM sync_runs WHERE id = ?`, runID).Scan(&runStatus); err != nil {
		t.Fatalf("query run status: %v", err)
	}
	if runStatus != "completed" {
		t.Fatalf("run status = %q, want completed", runStatus)
	}

	var action string
	if err := db.QueryRowContext(t.Context(), `SELECT action FROM sync_items WHERE sync_run_id = ? AND youtube_video_id = ?`, runID, "yt-video-1").Scan(&action); err != nil {
		t.Fatalf("query sync item action: %v", err)
	}
	if action != "added" {
		t.Fatalf("sync item action = %q, want added", action)
	}
	if len(committer.calls) != 1 || committer.calls[0] != int(runID) {
		t.Fatalf("commit calls = %#v, want [%d]", committer.calls, runID)
	}
}

func TestPendingCountInIndex(t *testing.T) {
	t.Parallel()

	db, _, mux := newTestHandler(t)
	repo := sqlite.NewMappingRepository(db)

	// Insert a playlist mapping.
	mapping := &domain.PlaylistMapping{
		YTPlaylistID:    "yt-pending-1",
		YTPlaylistTitle: "YT Pending Playlist",
		SPPlaylistID:    "sp-pending-1",
		SPPlaylistTitle: "SP Pending Playlist",
	}
	if err := repo.Save(t.Context(), mapping); err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	// Insert a completed sync run for that mapping.
	res, err := db.ExecContext(t.Context(), `
INSERT INTO sync_runs(mapping_id, started_at, finished_at, status)
VALUES(?, ?, ?, ?)
`, mapping.ID, time.Now().UTC().Add(-5*time.Minute), time.Now().UTC(), "completed")
	if err != nil {
		t.Fatalf("insert sync run: %v", err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id for sync run: %v", err)
	}

	// Insert two track_matches with decision = "pending".
	for _, vid := range []string{"yt-vid-p1", "yt-vid-p2"} {
		_, err = db.ExecContext(t.Context(), `
INSERT INTO track_matches(youtube_video_id, youtube_title, confidence, decision, created_at)
VALUES(?, ?, ?, ?, ?)
`, vid, "Some Title "+vid, 0.55, string(domain.MatchPending), time.Now().UTC())
		if err != nil {
			t.Fatalf("insert track match %s: %v", vid, err)
		}
	}

	// Insert sync_items linking both track_matches to the run.
	for _, vid := range []string{"yt-vid-p1", "yt-vid-p2"} {
		_, err = db.ExecContext(t.Context(), `
INSERT INTO sync_items(sync_run_id, youtube_video_id, action)
VALUES(?, ?, ?)
`, runID, vid, "pending_review")
		if err != nil {
			t.Fatalf("insert sync item %s: %v", vid, err)
		}
	}

	// GET / and check that the pending count badge is rendered.
	resp := performRequest(t, mux, http.MethodGet, "/", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := readBody(t, resp)
	if !strings.Contains(body, "2 Pending Review") {
		t.Fatalf("index body missing pending review badge: %q", body)
	}
}

func TestOAuthConnectFlow(t *testing.T) {
	t.Parallel()

	db, handler, mux := newTestHandler(t)
	handler.oauthTokenExchanger = func(ctx context.Context, conf *oauth2.Config, code string) (*oauth2.Token, error) {
		if code != "auth-code" {
			return nil, fmt.Errorf("oauth code = %q, want auth-code", code)
		}
		return &oauth2.Token{
			AccessToken:  "test-access-token",
			RefreshToken: "test-refresh-token",
			Expiry:       time.Now().UTC().Add(1 * time.Hour),
		}, nil
	}

	connectResp := performRequest(t, mux, http.MethodGet, "/oauth/youtube/connect", "", "")
	if connectResp.StatusCode != http.StatusFound {
		t.Fatalf("GET /oauth/youtube/connect status = %d, want %d", connectResp.StatusCode, http.StatusFound)
	}

	location := connectResp.Header.Get("Location")
	if !strings.Contains(location, "accounts.google.com") {
		t.Fatalf("connect redirect location = %q, want google oauth URL", location)
	}
	stateCookie := findCookie(connectResp, oauthStateCookieName(providerYouTube))
	if stateCookie == nil {
		t.Fatalf("missing oauth state cookie")
	}

	callbackReq := httptest.NewRequest(http.MethodGet, "/oauth/youtube/callback?code=auth-code&state="+stateCookie.Value, nil)
	callbackReq.Host = "localhost"
	callbackReq.AddCookie(stateCookie)
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

func newTestHandler(t *testing.T, syncServices ...syncCommitter) (*sql.DB, *Handler, *http.ServeMux) {
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

	h, err := NewHandler(
		db,
		sqlite.NewMappingRepository(db),
		"http://localhost:8080",
		"test-yt-client-id",
		"test-yt-secret",
		"test-sp-client-id",
		"test-sp-secret",
		syncServices...,
	)
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

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}

	return nil
}

type syncCommitterStub struct {
	commitFn func(ctx context.Context, syncRunID int) error
	calls    []int
}

func (s *syncCommitterStub) Commit(ctx context.Context, syncRunID int) error {
	s.calls = append(s.calls, syncRunID)
	if s.commitFn != nil {
		return s.commitFn(ctx, syncRunID)
	}
	return nil
}
