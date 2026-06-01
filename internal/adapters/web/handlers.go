package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bateau84/yt2sp/internal/adapters/spotify"
	"github.com/bateau84/yt2sp/internal/adapters/sqlite"
	"github.com/bateau84/yt2sp/internal/app"
	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/matcher"
	"github.com/bateau84/yt2sp/internal/ports"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	spotifyoauth "golang.org/x/oauth2/spotify"
)

const (
	providerYouTube = "youtube"
	providerSpotify = "spotify"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

type Handler struct {
	db                  *sql.DB
	mappingRepo         ports.MappingRepository
	syncService         syncCommitter
	templates           map[string]*template.Template
	oauthConfigs        map[string]*oauth2.Config
	oauthTokenExchanger func(ctx context.Context, conf *oauth2.Config, code string) (*oauth2.Token, error)
}

type syncCommitter interface {
	Commit(ctx context.Context, syncRunID int) error
}

type indexViewData struct {
	YouTubeConnected bool
	SpotifyConnected bool
	Mappings         []indexMappingView
	Flash            string
}

type indexMappingView struct {
	Mapping            domain.PlaylistMapping
	LatestRunID        int
	PendingReviewCount int
}

type mappingFormData struct {
	Flash string
	Error string
}

type syncRunView struct {
	ID                  int
	MappingID           int
	MappingYouTubeTitle string
	MappingSpotifyTitle string
	StartedAt           time.Time
	FinishedAt          *time.Time
	Status              string
}

type syncItemView struct {
	YouTubeVideoID string
	YouTubeTitle   string
	SpotifyTrackID string
	SpotifyTitle   string
	SpotifyArtist  string
	Confidence     float64
	Decision       string
	Action         string
	Error          string
}

type syncDetailData struct {
	Run          syncRunView
	Items        []syncItemView
	PendingCount int
	Flash        string
}

type reviewItemView struct {
	YouTubeVideoID string
	YouTubeTitle   string
	SpotifyTrackID string
	SpotifyTitle   string
	SpotifyArtist  string
	Confidence     float64
}

type matchReviewData struct {
	RunID int
	Items []reviewItemView
	Flash string
	Error string
}

func NewHandler(db *sql.DB, mappingRepo ports.MappingRepository, oauthBaseURL, ytClientID, ytSecret, spClientID, spSecret string, syncServices ...syncCommitter) (*Handler, error) {
	if db == nil {
		return nil, fmt.Errorf("web handler db is nil")
	}
	if mappingRepo == nil {
		return nil, fmt.Errorf("web handler mapping repo is nil")
	}

	var syncService syncCommitter
	if len(syncServices) > 0 {
		syncService = syncServices[0]
	}

	pages := []string{
		"index.gohtml",
		"mapping_form.gohtml",
		"sync_detail.gohtml",
		"match_review.gohtml",
	}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.ParseFS(templateFS, "templates/layout.gohtml", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", page, err)
		}
		tmpls[page] = t
	}

	return &Handler{
		db:          db,
		mappingRepo: mappingRepo,
		syncService: syncService,
		templates:   tmpls,
		oauthConfigs: map[string]*oauth2.Config{
			providerYouTube: {
				ClientID:     strings.TrimSpace(ytClientID),
				ClientSecret: strings.TrimSpace(ytSecret),
				Endpoint:     google.Endpoint,
				RedirectURL:  strings.TrimRight(oauthBaseURL, "/") + "/oauth/youtube/callback",
				Scopes: []string{
					"https://www.googleapis.com/auth/youtube.readonly",
				},
			},
			providerSpotify: {
				ClientID:     strings.TrimSpace(spClientID),
				ClientSecret: strings.TrimSpace(spSecret),
				Endpoint:     spotifyoauth.Endpoint,
				RedirectURL:  strings.TrimRight(oauthBaseURL, "/") + "/oauth/spotify/callback",
				Scopes: []string{
					"playlist-read-private",
					"playlist-modify-private",
					"playlist-modify-public",
				},
			},
		},
		oauthTokenExchanger: func(ctx context.Context, conf *oauth2.Config, code string) (*oauth2.Token, error) {
			return conf.Exchange(ctx, code)
		},
	}, nil
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.index)
	mux.HandleFunc("GET /mappings/new", h.createMappingForm)
	mux.HandleFunc("POST /mappings", h.createMapping)
	mux.HandleFunc("POST /mappings/{id}/delete", h.deleteMapping)
	mux.HandleFunc("GET /runs/{id}", h.syncDetail)
	mux.HandleFunc("POST /runs/{id}/confirm", h.confirmRun)
	mux.HandleFunc("GET /runs/{id}/review", h.matchReview)
	mux.HandleFunc("POST /runs/{id}/review/{decision}", h.updateMatchDecision)
	mux.HandleFunc("GET /oauth/{provider}/connect", h.oauthConnect)
	mux.HandleFunc("GET /oauth/{provider}/callback", h.oauthCallback)
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	mappings, err := h.mappingRepo.List(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("list mappings: %v", err), http.StatusInternalServerError)
		return
	}

	latestRunByMapping, err := h.latestRunIDsByMapping(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("list latest sync runs: %v", err), http.StatusInternalServerError)
		return
	}

	pendingCounts, err := h.pendingCountsByLatestRun(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("list pending review counts: %v", err), http.StatusInternalServerError)
		return
	}

	indexMappings := make([]indexMappingView, 0, len(mappings))
	for _, m := range mappings {
		indexMappings = append(indexMappings, indexMappingView{
			Mapping:            m,
			LatestRunID:        latestRunByMapping[m.ID],
			PendingReviewCount: pendingCounts[m.ID],
		})
	}

	ytConnected, err := h.providerConnected(ctx, providerYouTube)
	if err != nil {
		http.Error(w, fmt.Sprintf("read youtube connection status: %v", err), http.StatusInternalServerError)
		return
	}

	spConnected, err := h.providerConnected(ctx, providerSpotify)
	if err != nil {
		http.Error(w, fmt.Sprintf("read spotify connection status: %v", err), http.StatusInternalServerError)
		return
	}

	h.render(w, "index.gohtml", indexViewData{
		YouTubeConnected: ytConnected,
		SpotifyConnected: spConnected,
		Mappings:         indexMappings,
		Flash:            strings.TrimSpace(r.URL.Query().Get("flash")),
	})
}

func (h *Handler) createMappingForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, "mapping_form.gohtml", mappingFormData{Flash: strings.TrimSpace(r.URL.Query().Get("flash"))})
}

func (h *Handler) createMapping(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("parse form: %v", err), http.StatusBadRequest)
		return
	}

	ytID := strings.TrimSpace(r.FormValue("youtube_playlist_id"))
	spID := strings.TrimSpace(r.FormValue("spotify_playlist_id"))
	ytTitle := strings.TrimSpace(r.FormValue("youtube_playlist_title"))
	spTitle := strings.TrimSpace(r.FormValue("spotify_playlist_title"))

	if ytID == "" || spID == "" {
		w.WriteHeader(http.StatusBadRequest)
		h.render(w, "mapping_form.gohtml", mappingFormData{
			Error: "YouTube playlist ID and Spotify playlist ID are required.",
		})
		return
	}

	if ytTitle == "" {
		ytTitle = ytID
	}
	if spTitle == "" {
		spTitle = spID
	}

	mapping := &domain.PlaylistMapping{
		YTPlaylistID:    ytID,
		YTPlaylistTitle: ytTitle,
		SPPlaylistID:    spID,
		SPPlaylistTitle: spTitle,
	}

	if err := h.mappingRepo.Save(r.Context(), mapping); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		h.render(w, "mapping_form.gohtml", mappingFormData{
			Error: fmt.Sprintf("save mapping: %v", err),
		})
		return
	}

	http.Redirect(w, r, "/?flash=Mapping+created", http.StatusSeeOther)
}

func (h *Handler) deleteMapping(w http.ResponseWriter, r *http.Request) {
	mappingID, ok := parsePathInt(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	res, err := h.db.ExecContext(r.Context(), `DELETE FROM playlist_mappings WHERE id = ?`, mappingID)
	if err != nil {
		http.Error(w, fmt.Sprintf("delete mapping: %v", err), http.StatusInternalServerError)
		return
	}

	affected, err := res.RowsAffected()
	if err != nil {
		http.Error(w, fmt.Sprintf("delete mapping affected rows: %v", err), http.StatusInternalServerError)
		return
	}
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, "/?flash=Mapping+deleted", http.StatusSeeOther)
}

func (h *Handler) syncDetail(w http.ResponseWriter, r *http.Request) {
	runID, ok := parsePathInt(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	run, err := h.getSyncRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("load sync run: %v", err), http.StatusInternalServerError)
		return
	}

	items, err := h.listSyncItems(r.Context(), runID)
	if err != nil {
		http.Error(w, fmt.Sprintf("load sync items: %v", err), http.StatusInternalServerError)
		return
	}

	pendingCount, err := h.countPendingItems(r.Context(), runID)
	if err != nil {
		http.Error(w, fmt.Sprintf("count pending matches: %v", err), http.StatusInternalServerError)
		return
	}

	h.render(w, "sync_detail.gohtml", syncDetailData{
		Run:          *run,
		Items:        items,
		PendingCount: pendingCount,
		Flash:        strings.TrimSpace(r.URL.Query().Get("flash")),
	})
}

func (h *Handler) confirmRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := parsePathInt(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	syncService, err := h.getSyncService(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("confirm run: %v", err), http.StatusInternalServerError)
		return
	}

	if err := syncService.Commit(r.Context(), runID); err != nil {
		if errors.Is(err, app.ErrSyncRunNotFound) {
			http.NotFound(w, r)
			return
		}

		http.Error(w, fmt.Sprintf("confirm run: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/runs/%d?flash=Run+confirmed", runID), http.StatusSeeOther)
}

func (h *Handler) getSyncService(ctx context.Context) (syncCommitter, error) {
	if h.syncService != nil {
		return h.syncService, nil
	}

	token, err := h.getProviderToken(ctx, providerSpotify)
	if err != nil {
		return nil, err
	}

	spotifyService := spotify.NewAdapter(http.DefaultClient, token)
	matchRepo := sqlite.NewMatchRepository(h.db)
	h.syncService = app.NewSyncService(nil, spotifyService, h.mappingRepo, matchRepo, matcher.NewMatcher())

	return h.syncService, nil
}

func (h *Handler) getProviderToken(ctx context.Context, provider string) (string, error) {
	var accessToken sql.NullString
	err := h.db.QueryRowContext(ctx, `
SELECT access_token
FROM oauth_tokens
WHERE provider = ?
`, provider).Scan(&accessToken)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("oauth token for provider %q not found", provider)
	}
	if err != nil {
		return "", fmt.Errorf("query oauth token for provider %q: %w", provider, err)
	}

	token := strings.TrimSpace(accessToken.String)
	if token == "" {
		return "", fmt.Errorf("oauth token for provider %q is empty", provider)
	}

	return token, nil
}

func (h *Handler) matchReview(w http.ResponseWriter, r *http.Request) {
	runID, ok := parsePathInt(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	items, err := h.listPendingReviewItems(r.Context(), runID)
	if err != nil {
		http.Error(w, fmt.Sprintf("load pending review items: %v", err), http.StatusInternalServerError)
		return
	}

	h.render(w, "match_review.gohtml", matchReviewData{
		RunID: runID,
		Items: items,
		Flash: strings.TrimSpace(r.URL.Query().Get("flash")),
	})
}

func (h *Handler) updateMatchDecision(w http.ResponseWriter, r *http.Request) {
	runID, ok := parsePathInt(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	decisionPath := strings.ToLower(strings.TrimSpace(r.PathValue("decision")))
	decision := ""
	switch decisionPath {
	case "approve":
		decision = string(domain.MatchApproved)
	case "reject":
		decision = string(domain.MatchRejected)
	default:
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("parse decision form: %v", err), http.StatusBadRequest)
		return
	}

	ytVideoID := strings.TrimSpace(r.FormValue("youtube_video_id"))
	if ytVideoID == "" {
		http.Error(w, "youtube_video_id is required", http.StatusBadRequest)
		return
	}

	res, err := h.db.ExecContext(r.Context(), `
UPDATE track_matches
SET decision = ?, decision_source = 'user'
WHERE youtube_video_id = ?
  AND EXISTS (
	SELECT 1 FROM sync_items
	WHERE sync_run_id = ? AND youtube_video_id = ?
  )
`, decision, ytVideoID, runID, ytVideoID)
	if err != nil {
		http.Error(w, fmt.Sprintf("update match decision: %v", err), http.StatusInternalServerError)
		return
	}

	affected, err := res.RowsAffected()
	if err != nil {
		http.Error(w, fmt.Sprintf("update decision affected rows: %v", err), http.StatusInternalServerError)
		return
	}
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	flash := "Match+approved"
	if decision == string(domain.MatchRejected) {
		flash = "Match+rejected"
	}
	http.Redirect(w, r, fmt.Sprintf("/runs/%d/review?flash=%s", runID, flash), http.StatusSeeOther)
}

func (h *Handler) oauthConnect(w http.ResponseWriter, r *http.Request) {
	provider, ok := normalizeProvider(r.PathValue("provider"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	conf, ok := h.oauthConfigForProvider(provider)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(conf.ClientID) == "" || strings.TrimSpace(conf.ClientSecret) == "" {
		http.Error(w, fmt.Sprintf("oauth credentials for %s are not configured", provider), http.StatusBadRequest)
		return
	}

	state, err := newOAuthState()
	if err != nil {
		http.Error(w, fmt.Sprintf("generate oauth state: %v", err), http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName(provider),
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().UTC().Add(15 * time.Minute),
	})

	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	provider, ok := normalizeProvider(r.PathValue("provider"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	conf, ok := h.oauthConfigForProvider(provider)
	if !ok {
		http.NotFound(w, r)
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "missing oauth code", http.StatusBadRequest)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		http.Error(w, "missing oauth state", http.StatusBadRequest)
		return
	}

	stateCookie, err := r.Cookie(oauthStateCookieName(provider))
	if err != nil {
		http.Error(w, "missing oauth state cookie", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(stateCookie.Value) != state {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	token, err := h.oauthTokenExchanger(r.Context(), conf, code)
	if err != nil {
		http.Error(w, fmt.Sprintf("exchange oauth token: %v", err), http.StatusBadRequest)
		return
	}

	scopes := strings.TrimSpace(strings.Join(conf.Scopes, " "))
	if tokenScopes, ok := token.Extra("scope").(string); ok && strings.TrimSpace(tokenScopes) != "" {
		scopes = strings.TrimSpace(tokenScopes)
	}

	var expiry any
	if !token.Expiry.IsZero() {
		expiry = token.Expiry.UTC()
	}

	_, err = h.db.ExecContext(r.Context(), `
INSERT INTO oauth_tokens(provider, access_token, refresh_token, expiry, scopes, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
	access_token = excluded.access_token,
	refresh_token = excluded.refresh_token,
	expiry = excluded.expiry,
	scopes = excluded.scopes,
	updated_at = excluded.updated_at
`, provider, token.AccessToken, nullableString(token.RefreshToken), expiry, nullableString(scopes), time.Now().UTC())
	if err != nil {
		http.Error(w, fmt.Sprintf("save oauth token: %v", err), http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName(provider),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	http.Redirect(w, r, fmt.Sprintf("/?flash=%s+connected", provider), http.StatusSeeOther)
}

func (h *Handler) providerConnected(ctx context.Context, provider string) (bool, error) {
	var accessToken sql.NullString
	terr := h.db.QueryRowContext(ctx, `
SELECT access_token
FROM oauth_tokens
WHERE provider = ?
`, provider).Scan(&accessToken)
	if errors.Is(terr, sql.ErrNoRows) {
		return false, nil
	}
	if terr != nil {
		return false, fmt.Errorf("query oauth token: %w", terr)
	}

	return strings.TrimSpace(accessToken.String) != "", nil
}

func (h *Handler) getSyncRun(ctx context.Context, runID int) (*syncRunView, error) {
	row := h.db.QueryRowContext(ctx, `
SELECT sr.id,
	sr.mapping_id,
	pm.youtube_playlist_title,
	pm.spotify_playlist_title,
	sr.started_at,
	sr.finished_at,
	sr.status
FROM sync_runs sr
JOIN playlist_mappings pm ON pm.id = sr.mapping_id
WHERE sr.id = ?
`, runID)

	var run syncRunView
	var finishedAt sql.NullTime
	if err := row.Scan(
		&run.ID,
		&run.MappingID,
		&run.MappingYouTubeTitle,
		&run.MappingSpotifyTitle,
		&run.StartedAt,
		&finishedAt,
		&run.Status,
	); err != nil {
		return nil, err
	}
	if finishedAt.Valid {
		run.FinishedAt = &finishedAt.Time
	}

	return &run, nil
}

func (h *Handler) listSyncItems(ctx context.Context, runID int) ([]syncItemView, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT
	si.youtube_video_id,
	COALESCE(tm.youtube_title, ''),
	COALESCE(tm.spotify_track_id, ''),
	COALESCE(tm.spotify_track_title, ''),
	COALESCE(tm.spotify_artist, ''),
	COALESCE(tm.confidence, 0),
	COALESCE(tm.decision, ''),
	si.action,
	COALESCE(si.error, '')
FROM sync_items si
LEFT JOIN track_matches tm ON tm.youtube_video_id = si.youtube_video_id
WHERE si.sync_run_id = ?
ORDER BY si.id ASC
`, runID)
	if err != nil {
		return nil, fmt.Errorf("query sync items: %w", err)
	}
	defer rows.Close()

	var items []syncItemView
	for rows.Next() {
		var item syncItemView
		if err := rows.Scan(
			&item.YouTubeVideoID,
			&item.YouTubeTitle,
			&item.SpotifyTrackID,
			&item.SpotifyTitle,
			&item.SpotifyArtist,
			&item.Confidence,
			&item.Decision,
			&item.Action,
			&item.Error,
		); err != nil {
			return nil, fmt.Errorf("scan sync item: %w", err)
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sync items: %w", err)
	}

	return items, nil
}

func (h *Handler) countPendingItems(ctx context.Context, runID int) (int, error) {
	var count int
	err := h.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM sync_items si
JOIN track_matches tm ON tm.youtube_video_id = si.youtube_video_id
WHERE si.sync_run_id = ? AND tm.decision = ?
`, runID, string(domain.MatchPending)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("query pending item count: %w", err)
	}

	return count, nil
}

func (h *Handler) listPendingReviewItems(ctx context.Context, runID int) ([]reviewItemView, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT
	tm.youtube_video_id,
	tm.youtube_title,
	COALESCE(tm.spotify_track_id, ''),
	COALESCE(tm.spotify_track_title, ''),
	COALESCE(tm.spotify_artist, ''),
	tm.confidence
FROM sync_items si
JOIN track_matches tm ON tm.youtube_video_id = si.youtube_video_id
WHERE si.sync_run_id = ?
	AND tm.decision = ?
ORDER BY si.id ASC
`, runID, string(domain.MatchPending))
	if err != nil {
		return nil, fmt.Errorf("query pending review items: %w", err)
	}
	defer rows.Close()

	var items []reviewItemView
	for rows.Next() {
		var item reviewItemView
		if err := rows.Scan(
			&item.YouTubeVideoID,
			&item.YouTubeTitle,
			&item.SpotifyTrackID,
			&item.SpotifyTitle,
			&item.SpotifyArtist,
			&item.Confidence,
		); err != nil {
			return nil, fmt.Errorf("scan pending review item: %w", err)
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending review items: %w", err)
	}

	return items, nil
}

func (h *Handler) latestRunIDsByMapping(ctx context.Context) (map[int]int, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT mapping_id, MAX(id)
FROM sync_runs
GROUP BY mapping_id
`)
	if err != nil {
		return nil, fmt.Errorf("query latest run ids: %w", err)
	}
	defer rows.Close()

	result := make(map[int]int)
	for rows.Next() {
		var mappingID int
		var runID int
		if err := rows.Scan(&mappingID, &runID); err != nil {
			return nil, fmt.Errorf("scan latest run id row: %w", err)
		}

		result[mappingID] = runID
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest run id rows: %w", err)
	}

	return result, nil
}

// pendingCountsByLatestRun returns, for each mapping ID, the number of
// track_matches in a "pending" decision state that belong to that mapping's
// most recent sync run. A single query avoids N+1 calls.
func (h *Handler) pendingCountsByLatestRun(ctx context.Context) (map[int]int, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT sr.mapping_id, COUNT(tm.youtube_video_id)
FROM sync_runs sr
JOIN sync_items si ON si.sync_run_id = sr.id
JOIN track_matches tm ON tm.youtube_video_id = si.youtube_video_id
WHERE sr.id IN (SELECT MAX(id) FROM sync_runs GROUP BY mapping_id)
  AND tm.decision = ?
GROUP BY sr.mapping_id
`, string(domain.MatchPending))
	if err != nil {
		return nil, fmt.Errorf("query pending counts by latest run: %w", err)
	}
	defer rows.Close()

	result := make(map[int]int)
	for rows.Next() {
		var mappingID, count int
		if err := rows.Scan(&mappingID, &count); err != nil {
			return nil, fmt.Errorf("scan pending count row: %w", err)
		}
		result[mappingID] = count
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending count rows: %w", err)
	}

	return result, nil
}

func (h *Handler) render(w http.ResponseWriter, templateName string, data any) {
	t, ok := h.templates[templateName]
	if !ok {
		http.Error(w, "template not found: "+templateName, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.gohtml", data); err != nil {
		http.Error(w, fmt.Sprintf("render template %s: %v", templateName, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func normalizeProvider(provider string) (string, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == providerYouTube || provider == providerSpotify {
		return provider, true
	}

	return "", false
}

func parsePathInt(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}

	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}

	return n, true
}

func (h *Handler) oauthConfigForProvider(provider string) (*oauth2.Config, bool) {
	if h == nil || h.oauthConfigs == nil {
		return nil, false
	}

	conf, ok := h.oauthConfigs[provider]
	if !ok || conf == nil {
		return nil, false
	}

	return conf, true
}

func newOAuthState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

func oauthStateCookieName(provider string) string {
	return fmt.Sprintf("oauth_state_%s", provider)
}

func nullableString(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}

	return v
}
