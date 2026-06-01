package web

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/ports"
)

const (
	providerYouTube    = "youtube"
	providerSpotify    = "spotify"
	runStatusConfirmed = "confirmed"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

type Handler struct {
	db          *sql.DB
	mappingRepo ports.MappingRepository
	templates   *template.Template
}

type indexViewData struct {
	YouTubeConnected bool
	SpotifyConnected bool
	Mappings         []indexMappingView
	Flash            string
}

type indexMappingView struct {
	Mapping     domain.PlaylistMapping
	LatestRunID int
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

func NewHandler(db *sql.DB, mappingRepo ports.MappingRepository) (*Handler, error) {
	if db == nil {
		return nil, fmt.Errorf("web handler db is nil")
	}
	if mappingRepo == nil {
		return nil, fmt.Errorf("web handler mapping repo is nil")
	}

	tmpl, err := template.ParseFS(templateFS, "templates/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	return &Handler{
		db:          db,
		mappingRepo: mappingRepo,
		templates:   tmpl,
	}, nil
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.index)
	mux.HandleFunc("GET /mappings/new", h.createMappingForm)
	mux.HandleFunc("POST /mappings", h.createMapping)
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

	indexMappings := make([]indexMappingView, 0, len(mappings))
	for _, m := range mappings {
		indexMappings = append(indexMappings, indexMappingView{
			Mapping:     m,
			LatestRunID: latestRunByMapping[m.ID],
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

	res, err := h.db.ExecContext(r.Context(), `
UPDATE sync_runs
SET status = ?, finished_at = COALESCE(finished_at, ?)
WHERE id = ?
`, runStatusConfirmed, time.Now().UTC(), runID)
	if err != nil {
		http.Error(w, fmt.Sprintf("confirm run: %v", err), http.StatusInternalServerError)
		return
	}

	affected, err := res.RowsAffected()
	if err != nil {
		http.Error(w, fmt.Sprintf("confirm run affected rows: %v", err), http.StatusInternalServerError)
		return
	}
	if affected == 0 {
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/runs/%d?flash=Run+confirmed", runID), http.StatusSeeOther)
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

	callbackPath := fmt.Sprintf("/oauth/%s/callback", provider)
	callbackURL := h.absoluteURL(r, callbackPath)
	redirectURL := callbackURL + "?code=stub-auth-code&state=stub-state"

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	provider, ok := normalizeProvider(r.PathValue("provider"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	if strings.TrimSpace(r.URL.Query().Get("code")) == "" {
		http.Error(w, "missing oauth code", http.StatusBadRequest)
		return
	}

	_, err := h.db.ExecContext(r.Context(), `
INSERT INTO oauth_tokens(provider, access_token, refresh_token, expiry, scopes, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
	access_token = excluded.access_token,
	refresh_token = excluded.refresh_token,
	expiry = excluded.expiry,
	scopes = excluded.scopes,
	updated_at = excluded.updated_at
`, provider, "stub-access-token", "", time.Now().UTC().Add(24*time.Hour), "stub", time.Now().UTC())
	if err != nil {
		http.Error(w, fmt.Sprintf("save oauth token: %v", err), http.StatusInternalServerError)
		return
	}

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

func (h *Handler) render(w http.ResponseWriter, templateName string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, templateName, data); err != nil {
		http.Error(w, fmt.Sprintf("render template %s: %v", templateName, err), http.StatusInternalServerError)
	}
}

func (h *Handler) absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		scheme = forwardedProto
	} else if r.TLS != nil {
		scheme = "https"
	}

	return fmt.Sprintf("%s://%s%s", scheme, r.Host, path)
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
