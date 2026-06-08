package web

import (
	"context"
	"database/sql"
	"encoding/json"
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

// TestGetProviderTokenFresh_RefreshesExpiredToken verifies that when the stored
// access_token is expired, getProviderTokenFresh calls the token endpoint to
// obtain a new token and persists it back to the DB.
func TestGetProviderTokenFresh_RefreshesExpiredToken(t *testing.T) {
	t.Parallel()

	// Fake OAuth token endpoint that returns a new access token.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "new-refresh-token",
		})
	}))
	defer tokenServer.Close()

	db, h, _ := newTestHandler(t)

	// Override the spotify config to point at the fake token server.
	h.oauthConfigs[providerSpotify] = &oauth2.Config{
		ClientID:     "test-sp-client-id",
		ClientSecret: "test-sp-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: tokenServer.URL + "/token",
		},
	}

	// Insert an expired token with a valid refresh_token.
	_, err := db.ExecContext(t.Context(), `
INSERT INTO oauth_tokens(provider, access_token, refresh_token, expiry, scopes, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
	access_token = excluded.access_token,
	refresh_token = excluded.refresh_token,
	expiry = excluded.expiry,
	updated_at = excluded.updated_at
`, providerSpotify, "expired-token", "stored-refresh-token", time.Now().UTC().Add(-2*time.Hour), "stub", time.Now().UTC())
	if err != nil {
		t.Fatalf("insert expired token: %v", err)
	}

	accessToken, client, err := h.getProviderTokenFresh(t.Context(), providerSpotify)
	if err != nil {
		t.Fatalf("getProviderTokenFresh() error = %v", err)
	}
	if accessToken != "new-access-token" {
		t.Fatalf("accessToken = %q, want %q", accessToken, "new-access-token")
	}
	if client == nil {
		t.Fatal("getProviderTokenFresh() returned nil client")
	}

	// Verify the new token was persisted to the DB.
	var storedAccess, storedRefresh string
	if err := db.QueryRowContext(t.Context(), `
SELECT access_token, COALESCE(refresh_token, '')
FROM oauth_tokens WHERE provider = ?
`, providerSpotify).Scan(&storedAccess, &storedRefresh); err != nil {
		t.Fatalf("query stored token: %v", err)
	}
	if storedAccess != "new-access-token" {
		t.Errorf("stored access_token = %q, want %q", storedAccess, "new-access-token")
	}
	if storedRefresh != "new-refresh-token" {
		t.Errorf("stored refresh_token = %q, want %q", storedRefresh, "new-refresh-token")
	}
}

// TestGetProviderTokenFresh_ValidTokenNotRefreshed verifies that a non-expired
// token is returned as-is without hitting the token endpoint.
func TestGetProviderTokenFresh_ValidTokenNotRefreshed(t *testing.T) {
	t.Parallel()

	// The token server must NOT be called; if it is, the test fails.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("token endpoint called unexpectedly — valid token should not be refreshed")
		http.Error(w, "unexpected call", http.StatusInternalServerError)
	}))
	defer tokenServer.Close()

	db, h, _ := newTestHandler(t)

	h.oauthConfigs[providerSpotify] = &oauth2.Config{
		ClientID:     "test-sp-client-id",
		ClientSecret: "test-sp-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: tokenServer.URL + "/token",
		},
	}

	setProviderConnected(t, db, providerSpotify)

	accessToken, client, err := h.getProviderTokenFresh(t.Context(), providerSpotify)
	if err != nil {
		t.Fatalf("getProviderTokenFresh() error = %v", err)
	}
	if accessToken != "test-token" {
		t.Fatalf("accessToken = %q, want %q", accessToken, "test-token")
	}
	if client == nil {
		t.Fatal("getProviderTokenFresh() returned nil client")
	}
}

// TestGetProviderTokenFresh_MissingToken verifies that a missing DB row returns
// an appropriate error.
func TestGetProviderTokenFresh_MissingToken(t *testing.T) {
	t.Parallel()

	_, h, _ := newTestHandler(t)

	_, _, err := h.getProviderTokenFresh(t.Context(), providerSpotify)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// insertSyncFixtures inserts a mapping, sync run, sync item, and track match
// for use in pickCandidate tests. Returns the sync run ID.
func insertSyncFixtures(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
INSERT INTO playlist_mappings(youtube_playlist_id, youtube_playlist_title, spotify_playlist_id, spotify_playlist_title, created_at, updated_at)
VALUES ('yt-pl-1', 'YT Playlist', 'sp-pl-1', 'SP Playlist', datetime('now'), datetime('now'))
`)
	if err != nil {
		t.Fatalf("insert mapping: %v", err)
	}

	var mappingID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM playlist_mappings WHERE youtube_playlist_id = 'yt-pl-1'`).Scan(&mappingID); err != nil {
		t.Fatalf("get mapping id: %v", err)
	}

	res, err := db.ExecContext(ctx, `
INSERT INTO sync_runs(mapping_id, status, started_at) VALUES (?, 'pending', datetime('now'))
`, mappingID)
	if err != nil {
		t.Fatalf("insert sync run: %v", err)
	}
	runID, _ := res.LastInsertId()

	// track_match must exist before sync_item due to FK: sync_items.youtube_video_id → track_matches.youtube_video_id
	_, err = db.ExecContext(ctx, `
INSERT INTO track_matches(youtube_video_id, youtube_title, spotify_track_id, spotify_track_title, spotify_artist, confidence, decision, decision_source)
VALUES ('yt-vid-1', 'Original Title', 'sp-old-track', 'Old Track', 'Old Artist', 0.75, 'auto', 'auto')
`)
	if err != nil {
		t.Fatalf("insert track match: %v", err)
	}

	_, err = db.ExecContext(ctx, `
INSERT INTO sync_items(sync_run_id, youtube_video_id, action) VALUES (?, 'yt-vid-1', 'add')
`, runID)
	if err != nil {
		t.Fatalf("insert sync item: %v", err)
	}

	return runID
}

// TestPickCandidate_Once verifies that mode="once" updates only sync_items and
// leaves track_matches unchanged globally.
func TestPickCandidate_Once(t *testing.T) {
	t.Parallel()

	db, _, mux := newTestHandler(t)
	runID := insertSyncFixtures(t, db)

	resp := performRequest(t, mux, http.MethodPost,
		fmt.Sprintf("/runs/%d/review/pick", runID),
		url.Values{
			"youtube_video_id": {"yt-vid-1"},
			"spotify_track_id": {"sp-new-track"},
			"spotify_title":    {"New Track"},
			"spotify_artist":   {"New Artist"},
			"mode":             {"once"},
		}.Encode(),
		"application/x-www-form-urlencoded",
	)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST pick status = %d, want %d; body: %s", resp.StatusCode, http.StatusSeeOther, readBody(t, resp))
	}

	// sync_items.selected_spotify_track_id must be updated.
	var selectedID string
	err := db.QueryRowContext(t.Context(), `
SELECT COALESCE(selected_spotify_track_id, '') FROM sync_items WHERE sync_run_id = ? AND youtube_video_id = 'yt-vid-1'
`, runID).Scan(&selectedID)
	if err != nil {
		t.Fatalf("query sync_items: %v", err)
	}
	if selectedID != "sp-new-track" {
		t.Errorf("sync_items.selected_spotify_track_id = %q, want %q", selectedID, "sp-new-track")
	}

	// track_matches must NOT be modified — original values preserved.
	var tmTrackID, tmSource string
	err = db.QueryRowContext(t.Context(), `
SELECT spotify_track_id, decision_source FROM track_matches WHERE youtube_video_id = 'yt-vid-1'
`).Scan(&tmTrackID, &tmSource)
	if err != nil {
		t.Fatalf("query track_matches: %v", err)
	}
	if tmTrackID != "sp-old-track" {
		t.Errorf("track_matches.spotify_track_id = %q, want %q (must be unchanged)", tmTrackID, "sp-old-track")
	}
	if tmSource != "auto" {
		t.Errorf("track_matches.decision_source = %q, want %q (must be unchanged)", tmSource, "auto")
	}
}

// TestPickCandidate_Remember verifies that mode="remember" updates track_matches
// globally with decision_source='user' and leaves sync_items.selected_spotify_track_id alone.
func TestPickCandidate_Remember(t *testing.T) {
	t.Parallel()

	db, _, mux := newTestHandler(t)
	runID := insertSyncFixtures(t, db)

	resp := performRequest(t, mux, http.MethodPost,
		fmt.Sprintf("/runs/%d/review/pick", runID),
		url.Values{
			"youtube_video_id": {"yt-vid-1"},
			"spotify_track_id": {"sp-remembered-track"},
			"spotify_title":    {"Remembered Track"},
			"spotify_artist":   {"Remembered Artist"},
			"mode":             {"remember"},
		}.Encode(),
		"application/x-www-form-urlencoded",
	)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST pick status = %d, want %d; body: %s", resp.StatusCode, http.StatusSeeOther, readBody(t, resp))
	}

	// track_matches must be updated globally with decision_source='user'.
	var tmTrackID, tmDecision, tmSource string
	err := db.QueryRowContext(t.Context(), `
SELECT spotify_track_id, decision, decision_source FROM track_matches WHERE youtube_video_id = 'yt-vid-1'
`).Scan(&tmTrackID, &tmDecision, &tmSource)
	if err != nil {
		t.Fatalf("query track_matches: %v", err)
	}
	if tmTrackID != "sp-remembered-track" {
		t.Errorf("track_matches.spotify_track_id = %q, want %q", tmTrackID, "sp-remembered-track")
	}
	if tmDecision != "approved" {
		t.Errorf("track_matches.decision = %q, want %q", tmDecision, "approved")
	}
	if tmSource != "user" {
		t.Errorf("track_matches.decision_source = %q, want %q", tmSource, "user")
	}

	// sync_items.selected_spotify_track_id must NOT be set by "remember".
	var selectedID sql.NullString
	err = db.QueryRowContext(t.Context(), `
SELECT selected_spotify_track_id FROM sync_items WHERE sync_run_id = ? AND youtube_video_id = 'yt-vid-1'
`, runID).Scan(&selectedID)
	if err != nil {
		t.Fatalf("query sync_items: %v", err)
	}
	if selectedID.Valid && selectedID.String != "" {
		t.Errorf("sync_items.selected_spotify_track_id = %q, want empty (remember must not set it)", selectedID.String)
	}
}

// TestPickCandidate_InvalidMode verifies that an unrecognised mode returns 400.
func TestPickCandidate_InvalidMode(t *testing.T) {
	t.Parallel()

	db, _, mux := newTestHandler(t)
	runID := insertSyncFixtures(t, db)

	resp := performRequest(t, mux, http.MethodPost,
		fmt.Sprintf("/runs/%d/review/pick", runID),
		url.Values{
			"youtube_video_id": {"yt-vid-1"},
			"spotify_track_id": {"sp-track"},
			"mode":             {"bogus"},
		}.Encode(),
		"application/x-www-form-urlencoded",
	)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	readBody(t, resp) // drain
}
