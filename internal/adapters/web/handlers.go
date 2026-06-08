package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bateau84/yt2sp/internal/adapters/spotify"
	"github.com/bateau84/yt2sp/internal/adapters/sqlite"
	"github.com/bateau84/yt2sp/internal/adapters/youtube"
	"github.com/bateau84/yt2sp/internal/app"
	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/matcher"
	"github.com/bateau84/yt2sp/internal/ports"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/sync/errgroup"
	spotifyoauth "golang.org/x/oauth2/spotify"
)

const (
	providerYouTube = "youtube"
	providerSpotify = "spotify"
)

// Dry-run lifecycle statuses used when the web handler creates or updates sync runs.
// These must match the status values used in the app layer and stored in the DB.
const (
	syncRunStatusRunning   = "running"
	syncRunStatusCompleted = "completed"
	syncRunStatusFailed    = "failed"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

type Handler struct {
	db            *sql.DB
	mappingRepo   ports.MappingRepository
	matchRepo     ports.MatchRepository
	candidateRepo ports.CandidateRepository
	eventRepo     ports.SyncRunEventRepository
	syncService   syncCommitter
	ytService     ports.YouTubeService
	spService     ports.SpotifyService
	// asyncRunner performs the background dry sync. Set in production by
	// buildAsyncRunner; overridable in tests.
	asyncRunner asyncDryRunner
	// asyncJobDone is an optional test hook invoked (with defer) at the very
	// end of the background goroutine, after all DB writes are attempted.
	// It is nil in production and must never block the caller.
	asyncJobDone  func(runID int)
	templates     map[string]*template.Template
	oauthConfigs  map[string]*oauth2.Config
	oauthTokenExchanger func(ctx context.Context, conf *oauth2.Config, code string) (*oauth2.Token, error)
}

type syncCommitter interface {
	Commit(ctx context.Context, syncRunID int) error
}

// asyncDryRunner performs a dry sync into a pre-created run.
// The run must already exist in the DB before this is called.
type asyncDryRunner interface {
	RunDryInto(ctx context.Context, run *domain.SyncRun, mappingID int) ([]domain.TrackMatch, error)
}

// dbProgressReporter implements ports.ProgressReporter by persisting events
// to the sync_run_events table via the event repository.
type dbProgressReporter struct {
	eventRepo ports.SyncRunEventRepository
}

func (r *dbProgressReporter) Report(ctx context.Context, runID int, level, message string) {
	ev := &domain.SyncRunEvent{
		RunID:   runID,
		Level:   level,
		Message: message,
	}
	// Best-effort: errors are logged but do not abort the sync.
	if err := r.eventRepo.SaveSyncRunEvent(ctx, ev); err != nil {
		slog.Warn("failed to save sync run event", "run_id", runID, "level", level, "error", err)
	}
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
	Flash             string
	Error             string
	YouTubePlaylists  []ports.PlaylistSummary
	SpotifyPlaylists  []ports.PlaylistSummary
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
	YouTubeURL     string // https://www.youtube.com/watch?v=<YTVideoID>
	SpotifyTrackID string
	SpotifyTitle   string
	SpotifyArtist  string
	SpotifyURL     string // https://open.spotify.com/track/<SPTrackID>
	Confidence     float64
	Decision       string
	IsPriorChoice  bool
	IsAutoMatch    bool // true when decision="auto"; controls initial expand state of alternatives
	// Candidates holds the top alternative matches from the current run.
	Candidates []candidateView
}

func (v reviewItemView) IsResolved() bool {
	return v.IsApproved() || v.IsRejected()
}

func (v reviewItemView) IsApproved() bool {
	return domain.MatchDecision(v.Decision) == domain.MatchApproved
}

func (v reviewItemView) IsRejected() bool {
	return domain.MatchDecision(v.Decision) == domain.MatchRejected
}

// candidateView is one Spotify search result shown as an alternative.
type candidateView struct {
	Rank           int
	SPTrackID      string
	SPTitle        string
	SPArtist       string
	SpotifyURL     string
	Confidence     float64
}

type matchReviewData struct {
	RunID int
	Items []reviewItemView
	Flash string
	Error string
}

// syncProgressData is the data passed to the sync_progress.gohtml template.
type syncProgressData struct {
	Run          syncRunView
	PendingCount int
}

// syncProgressEventsResponse is the JSON payload returned by GET /runs/{id}/events.
type syncProgressEventsResponse struct {
	Run          syncProgressRunJSON   `json:"run"`
	Events       []syncProgressEventJSON `json:"events"`
	PendingCount int                   `json:"pending_count"`
	URLs         syncProgressURLsJSON  `json:"urls"`
}

type syncProgressRunJSON struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
}

type syncProgressEventJSON struct {
	ID        int    `json:"id"`
	CreatedAt string `json:"created_at"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Details   string `json:"details"`
}

type syncProgressURLsJSON struct {
	Results string `json:"results"`
	Review  string `json:"review"`
	Routes  string `json:"routes"`
}

func NewHandler(db *sql.DB, mappingRepo ports.MappingRepository, oauthBaseURL, ytClientID, ytSecret, spClientID, spSecret string, ytService ports.YouTubeService, spService ports.SpotifyService, syncServices ...syncCommitter) (*Handler, error) {
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
		"sync_progress.gohtml",
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
		db:            db,
		mappingRepo:   mappingRepo,
		matchRepo:     sqlite.NewMatchRepository(db),
		candidateRepo: sqlite.NewCandidateRepository(db),
		eventRepo:     sqlite.NewSyncRunEventRepository(db),
		syncService:   syncService,
		ytService:     ytService,
		spService:     spService,
		templates:     tmpls,
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
	mux.HandleFunc("POST /mappings/{id}/sync", h.runDrySync)
	mux.HandleFunc("POST /mappings/{id}/delete", h.deleteMapping)
	mux.HandleFunc("GET /runs/{id}", h.syncDetail)
	mux.HandleFunc("GET /runs/{id}/progress", h.syncProgress)
	mux.HandleFunc("GET /runs/{id}/events", h.syncProgressEvents)
	mux.HandleFunc("POST /runs/{id}/confirm", h.confirmRun)
	mux.HandleFunc("GET /runs/{id}/review", h.matchReview)
	mux.HandleFunc("POST /runs/{id}/review/{decision}", h.updateMatchDecision)
	// pick allows choosing an alternative candidate as "use once" (run-scoped) or "remember" (global).
	mux.HandleFunc("POST /runs/{id}/review/pick", h.pickCandidate)
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
	ctx := r.Context()
	var ytPlaylists, spPlaylists []ports.PlaylistSummary

	g, gctx := errgroup.WithContext(ctx)
	if h.ytService != nil {
		g.Go(func() error {
			pl, err := h.ytService.ListUserPlaylists(gctx)
			if err != nil {
				slog.Warn("failed to fetch YouTube playlists", "error", err)
				return nil
			}
			ytPlaylists = pl
			return nil
		})
	}
	if h.spService != nil {
		g.Go(func() error {
			pl, err := h.spService.ListUserPlaylists(gctx)
			if err != nil {
				slog.Warn("failed to fetch Spotify playlists", "error", err)
				return nil
			}
			spPlaylists = pl
			return nil
		})
	}
	_ = g.Wait()

	h.render(w, "mapping_form.gohtml", mappingFormData{
		Flash:             strings.TrimSpace(r.URL.Query().Get("flash")),
		YouTubePlaylists:  ytPlaylists,
		SpotifyPlaylists:  spPlaylists,
	})
}

func (h *Handler) createMapping(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("parse form: %v", err), http.StatusBadRequest)
		return
	}

	ytSelect := strings.TrimSpace(r.FormValue("youtube_playlist_id_select"))
	spSelect := strings.TrimSpace(r.FormValue("spotify_playlist_id_select"))

	ytID := ytSelect
	if ytSelect == "__manual__" {
		ytID = strings.TrimSpace(r.FormValue("youtube_playlist_id"))
	} else if ytSelect == "" {
		// Backward compat: if no select field, use direct input
		ytID = strings.TrimSpace(r.FormValue("youtube_playlist_id"))
	}

	spID := spSelect
	if spSelect == "__manual__" {
		spID = strings.TrimSpace(r.FormValue("spotify_playlist_id"))
	} else if spSelect == "" {
		// Backward compat: if no select field, use direct input
		spID = strings.TrimSpace(r.FormValue("spotify_playlist_id"))
	}

	ytTitle := strings.TrimSpace(r.FormValue("youtube_playlist_title"))
	spTitle := strings.TrimSpace(r.FormValue("spotify_playlist_title"))

	if ytID == "" || spID == "" {
		w.WriteHeader(http.StatusBadRequest)
		h.renderFormWithError(w, r, "YouTube playlist ID and Spotify playlist ID are required.")
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
		h.renderFormWithError(w, r, fmt.Sprintf("save mapping: %v", err))
		return
	}

	http.Redirect(w, r, "/?flash=Mapping+created", http.StatusSeeOther)
}

func (h *Handler) renderFormWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	ctx := r.Context()
	var ytPlaylists, spPlaylists []ports.PlaylistSummary

	g, gctx := errgroup.WithContext(ctx)
	if h.ytService != nil {
		g.Go(func() error {
			pl, err := h.ytService.ListUserPlaylists(gctx)
			if err != nil {
				slog.Warn("failed to fetch YouTube playlists", "error", err)
				return nil
			}
			ytPlaylists = pl
			return nil
		})
	}
	if h.spService != nil {
		g.Go(func() error {
			pl, err := h.spService.ListUserPlaylists(gctx)
			if err != nil {
				slog.Warn("failed to fetch Spotify playlists", "error", err)
				return nil
			}
			spPlaylists = pl
			return nil
		})
	}
	_ = g.Wait()

	h.render(w, "mapping_form.gohtml", mappingFormData{
		Error:            errMsg,
		YouTubePlaylists: ytPlaylists,
		SpotifyPlaylists: spPlaylists,
	})
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

// syncProgress renders the progress page for a sync run.
func (h *Handler) syncProgress(w http.ResponseWriter, r *http.Request) {
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

	pendingCount, err := h.countPendingItems(r.Context(), runID)
	if err != nil {
		http.Error(w, fmt.Sprintf("count pending items: %v", err), http.StatusInternalServerError)
		return
	}

	h.render(w, "sync_progress.gohtml", syncProgressData{
		Run:          *run,
		PendingCount: pendingCount,
	})
}

// syncProgressEvents returns a JSON payload with run status, events, pending count, and action URLs.
func (h *Handler) syncProgressEvents(w http.ResponseWriter, r *http.Request) {
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

	events, err := h.eventRepo.ListSyncRunEvents(r.Context(), runID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list sync run events: %v", err), http.StatusInternalServerError)
		return
	}

	pendingCount, err := h.countPendingItems(r.Context(), runID)
	if err != nil {
		http.Error(w, fmt.Sprintf("count pending items: %v", err), http.StatusInternalServerError)
		return
	}

	evJSON := make([]syncProgressEventJSON, len(events))
	for i, ev := range events {
		evJSON[i] = syncProgressEventJSON{
			ID:        ev.ID,
			CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
			Level:     ev.Level,
			Message:   ev.Message,
			Details:   ev.Details,
		}
	}

	resp := syncProgressEventsResponse{
		Run: syncProgressRunJSON{
			ID:     run.ID,
			Status: run.Status,
		},
		Events:       evJSON,
		PendingCount: pendingCount,
		URLs: syncProgressURLsJSON{
			Results: fmt.Sprintf("/runs/%d", runID),
			Review:  fmt.Sprintf("/runs/%d/review", runID),
			Routes:  "/",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Header already set; log only.
		_ = err
	}
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

	spToken, spClient, err := h.getProviderTokenFresh(ctx, providerSpotify)
	if err != nil {
		return nil, err
	}

	spotifyService := spotify.NewAdapter(spClient, spToken)
	matchRepo := sqlite.NewMatchRepository(h.db)
	return app.NewSyncService(nil, spotifyService, h.mappingRepo, matchRepo, matcher.NewMatcher()), nil
}

// buildAsyncRunner constructs a SyncService wired for background dry-sync.
// It fetches OAuth tokens using the provided context (from the HTTP request),
// then configures the service with a dbProgressReporter so events are persisted.
func (h *Handler) buildAsyncRunner(ctx context.Context) (asyncDryRunner, error) {
	ytToken, ytClient, err := h.getProviderTokenFresh(ctx, providerYouTube)
	if err != nil {
		return nil, fmt.Errorf("get youtube token: %w", err)
	}
	spToken, spClient, err := h.getProviderTokenFresh(ctx, providerSpotify)
	if err != nil {
		return nil, fmt.Errorf("get spotify token: %w", err)
	}

	ytService := youtube.NewAdapter(ytClient, ytToken)
	spService := spotify.NewAdapter(spClient, spToken)
	matchRepo := sqlite.NewMatchRepository(h.db)
	candidateRepo := sqlite.NewCandidateRepository(h.db)
	return app.NewSyncService(
		ytService, spService, h.mappingRepo, matchRepo, matcher.NewMatcher(),
		app.WithCandidateRepository(candidateRepo),
		app.WithProgressReporter(&dbProgressReporter{eventRepo: h.eventRepo}),
	), nil
}

// persistSyncItems inserts a sync_items row for each match returned by RunDry/RunDryInto.
// action='pending_review' marks items awaiting user review in the UI.
func (h *Handler) persistSyncItems(ctx context.Context, runID int, matches []domain.TrackMatch) error {
	for _, m := range matches {
		if _, err := h.db.ExecContext(ctx, `
INSERT INTO sync_items(sync_run_id, youtube_video_id, action)
VALUES (?, ?, 'pending_review')
`, runID, m.YTVideoID); err != nil {
			return fmt.Errorf("save sync item for video %q: %w", m.YTVideoID, err)
		}
	}
	return nil
}

func (h *Handler) runDrySync(w http.ResponseWriter, r *http.Request) {
	mappingID, ok := parsePathInt(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Guard: if a running dry sync already exists for this mapping, redirect
	// to its progress page instead of creating a duplicate.
	var existingRunID int
	guardErr := h.db.QueryRowContext(r.Context(), `
SELECT id FROM sync_runs
WHERE mapping_id = ? AND status = ?
ORDER BY started_at DESC, id DESC
LIMIT 1
`, mappingID, syncRunStatusRunning).Scan(&existingRunID)
	if guardErr == nil {
		// An active run exists — redirect to it.
		http.Redirect(w, r, fmt.Sprintf("/runs/%d/progress", existingRunID), http.StatusSeeOther)
		return
	}
	if !errors.Is(guardErr, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("check existing sync run: %v", guardErr), http.StatusInternalServerError)
		return
	}

	// Resolve the async runner — prefer injected test stub, fall back to building
	// a real one from OAuth tokens.
	runner := h.asyncRunner
	if runner == nil {
		var err error
		runner, err = h.buildAsyncRunner(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("build sync service: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Create the run synchronously so it exists before we redirect.
	res, err := h.db.ExecContext(r.Context(), `
INSERT INTO sync_runs(mapping_id, status, started_at)
VALUES (?, ?, datetime('now'))
`, mappingID, syncRunStatusRunning)
	if err != nil {
		http.Error(w, fmt.Sprintf("create sync run: %v", err), http.StatusInternalServerError)
		return
	}
	runIDRaw, err := res.LastInsertId()
	if err != nil {
		http.Error(w, fmt.Sprintf("get run id: %v", err), http.StatusInternalServerError)
		return
	}
	runID := int(runIDRaw)

	run := &domain.SyncRun{
		ID:        runID,
		MappingID: mappingID,
		Status:    syncRunStatusRunning,
	}

	// Detach from request context so the goroutine outlives the HTTP response.
	// Add a 30-minute cap to prevent runaway background jobs.
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)

	// Capture hook locally so the goroutine closure doesn't race on h.asyncJobDone.
	jobDone := h.asyncJobDone

	go func() {
		defer cancel()
		if jobDone != nil {
			defer jobDone(runID)
		}

		matches, dryErr := runner.RunDryInto(bgCtx, run, mappingID)
		if dryErr != nil {
			// Persist an error event and mark the run failed.
			ev := &domain.SyncRunEvent{RunID: runID, Level: "error", Message: fmt.Sprintf("Dry sync failed: %v", dryErr)}
			if saveErr := h.eventRepo.SaveSyncRunEvent(bgCtx, ev); saveErr != nil {
				slog.Warn("failed to save error event", "run_id", runID, "error", saveErr)
			}
			if _, updateErr := h.db.ExecContext(bgCtx, `
UPDATE sync_runs SET status = ?, finished_at = datetime('now') WHERE id = ?
`, syncRunStatusFailed, runID); updateErr != nil {
				slog.Warn("failed to mark run failed", "run_id", runID, "error", updateErr)
			}
			return
		}

		// Persist sync_items for the matched tracks.
		if itemErr := h.persistSyncItems(bgCtx, runID, matches); itemErr != nil {
			slog.Warn("failed to persist sync items", "run_id", runID, "error", itemErr)
		}

		// Mark the run completed.
		if _, updateErr := h.db.ExecContext(bgCtx, `
UPDATE sync_runs SET status = ?, finished_at = datetime('now') WHERE id = ?
`, syncRunStatusCompleted, runID); updateErr != nil {
			slog.Warn("failed to mark run completed", "run_id", runID, "error", updateErr)
		}
	}()

	http.Redirect(w, r, fmt.Sprintf("/runs/%d/progress", runID), http.StatusSeeOther)
}

// getProviderTokenFresh loads the stored OAuth token for provider, refreshes it
// if expired using the configured oauth2.Config, persists any newly issued
// tokens back to the DB, and returns the current access token together with an
// auto-refreshing *http.Client.
func (h *Handler) getProviderTokenFresh(ctx context.Context, provider string) (accessToken string, client *http.Client, err error) {
	conf, ok := h.oauthConfigForProvider(provider)
	if !ok {
		return "", nil, fmt.Errorf("no oauth config for provider %q", provider)
	}

	var (
		rawAccess  sql.NullString
		rawRefresh sql.NullString
		rawExpiry  sql.NullTime
	)
	err = h.db.QueryRowContext(ctx, `
SELECT access_token, refresh_token, expiry
FROM oauth_tokens
WHERE provider = ?
`, provider).Scan(&rawAccess, &rawRefresh, &rawExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("oauth token for provider %q not found", provider)
	}
	if err != nil {
		return "", nil, fmt.Errorf("query oauth token for provider %q: %w", provider, err)
	}

	stored := strings.TrimSpace(rawAccess.String)
	if stored == "" {
		return "", nil, fmt.Errorf("oauth token for provider %q is empty", provider)
	}

	tok := &oauth2.Token{
		AccessToken:  stored,
		RefreshToken: strings.TrimSpace(rawRefresh.String),
		Expiry:       rawExpiry.Time, // zero value when !rawExpiry.Valid; oauth2 treats zero as "no expiry"
	}

	// Wrap the token source so any token refresh is immediately saved to DB.
	ts := &persistingTokenSource{
		base:     conf.TokenSource(ctx, tok),
		db:       h.db,
		ctx:      ctx,
		provider: provider,
	}

	// Token() refreshes if expired; uses stored token if still valid.
	fresh, err := ts.Token()
	if err != nil {
		return "", nil, fmt.Errorf("refresh oauth token for provider %q: %w", provider, err)
	}

	return fresh.AccessToken, oauth2.NewClient(ctx, ts), nil
}

// persistingTokenSource wraps an oauth2.TokenSource and saves any newly issued
// token (i.e. a refreshed token) back to the oauth_tokens DB table so the
// refreshed credentials survive across restarts.
type persistingTokenSource struct {
	base     oauth2.TokenSource
	db       *sql.DB
	ctx      context.Context //nolint:containedctx // stored for DB writes that happen outside request scope
	provider string
	last     *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		return nil, err
	}

	// Persist only when a new token has been issued (access token changed).
	if p.last == nil || p.last.AccessToken != tok.AccessToken {
		var expiry any
		if !tok.Expiry.IsZero() {
			expiry = tok.Expiry.UTC()
		}
		_, dbErr := p.db.ExecContext(p.ctx, `
UPDATE oauth_tokens
SET access_token = ?, refresh_token = ?, expiry = ?, updated_at = ?
WHERE provider = ?
`, tok.AccessToken, nullableString(tok.RefreshToken), expiry, time.Now().UTC(), p.provider)
		if dbErr != nil {
			// Log and continue — the in-memory token is still usable for this request.
			// The next request will re-fetch and retry the refresh if needed.
			_ = dbErr
		}
		p.last = tok
	}

	return tok, nil
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
	http.Redirect(w, r, fmt.Sprintf("/runs/%d/review?flash=%s#match-%s", runID, flash, ytVideoID), http.StatusSeeOther)
}

// pickCandidate handles POST /runs/{id}/review/pick.
// It lets a reviewer select an alternative Spotify candidate instead of the
// current best match. Mode is "once" (run-scoped only) or "remember" (persisted
// globally so future dry runs skip re-searching).
func (h *Handler) pickCandidate(w http.ResponseWriter, r *http.Request) {
	runID, ok := parsePathInt(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("parse pick form: %v", err), http.StatusBadRequest)
		return
	}

	ytVideoID := strings.TrimSpace(r.FormValue("youtube_video_id"))
	spTrackID := strings.TrimSpace(r.FormValue("spotify_track_id"))
	spTitle := strings.TrimSpace(r.FormValue("spotify_title"))
	spArtist := strings.TrimSpace(r.FormValue("spotify_artist"))
	mode := strings.ToLower(strings.TrimSpace(r.FormValue("mode"))) // "once" or "remember"

	if ytVideoID == "" || spTrackID == "" {
		http.Error(w, "youtube_video_id and spotify_track_id are required", http.StatusBadRequest)
		return
	}
	if mode != "once" && mode != "remember" {
		http.Error(w, "mode must be 'once' or 'remember'", http.StatusBadRequest)
		return
	}

	if mode == "once" {
		// Run-scoped override: only update sync_items for this run.
		// The global track_matches record is left untouched so the match
		// remains as-is for future dry runs.
		// ListSyncRunMatches uses COALESCE(tm.spotify_track_id, si.selected_spotify_track_id)
		// so commit will pick up this run-local selection.
		_, err := h.db.ExecContext(r.Context(), `
UPDATE sync_items
SET selected_spotify_track_id = ?
WHERE sync_run_id = ? AND youtube_video_id = ?
`, spTrackID, runID, ytVideoID)
		if err != nil {
			http.Error(w, fmt.Sprintf("update sync item track (once): %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		// "remember": persist the choice globally via the match repository so
		// future dry runs recognise it as a prior manual decision (IsPriorChoice=true).
		// First verify the video belongs to this run to prevent cross-run mutations.
		var exists int
		err := h.db.QueryRowContext(r.Context(), `
SELECT COUNT(1) FROM sync_items WHERE sync_run_id = ? AND youtube_video_id = ?
`, runID, ytVideoID).Scan(&exists)
		if err != nil {
			http.Error(w, fmt.Sprintf("verify run ownership: %v", err), http.StatusInternalServerError)
			return
		}
		if exists == 0 {
			http.NotFound(w, r)
			return
		}
		if err := h.matchRepo.UpdateMatchChoice(r.Context(), ytVideoID, spTrackID, spTitle, spArtist, domain.MatchApproved); err != nil {
			http.Error(w, fmt.Sprintf("update picked candidate (remember): %v", err), http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, fmt.Sprintf("/runs/%d/review?flash=Candidate+picked#match-%s", runID, ytVideoID), http.StatusSeeOther)
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
	// Return all items associated with the sync run so the review queue remains
	// stable (resolved items stay in their original playlist position).
	rows, err := h.db.QueryContext(ctx, `
SELECT
	tm.youtube_video_id,
	tm.youtube_title,
	COALESCE(tm.spotify_track_id, ''),
	COALESCE(tm.spotify_track_title, ''),
	COALESCE(tm.spotify_artist, ''),
	tm.confidence,
	COALESCE(tm.decision_source, ''),
	tm.decision
FROM sync_items si
JOIN track_matches tm ON tm.youtube_video_id = si.youtube_video_id
WHERE si.sync_run_id = ?
ORDER BY si.id ASC
`, runID)
	if err != nil {
		return nil, fmt.Errorf("query pending review items: %w", err)
	}
	defer rows.Close()

	var items []reviewItemView
	for rows.Next() {
		var item reviewItemView
		var decisionSource string
		if err := rows.Scan(
			&item.YouTubeVideoID,
			&item.YouTubeTitle,
			&item.SpotifyTrackID,
			&item.SpotifyTitle,
			&item.SpotifyArtist,
			&item.Confidence,
			&decisionSource,
			&item.Decision,
		); err != nil {
			return nil, fmt.Errorf("scan pending review item: %w", err)
		}
		item.YouTubeURL = youtubeVideoURL(item.YouTubeVideoID)
		item.SpotifyURL = spotifyTrackURL(item.SpotifyTrackID)
		item.IsPriorChoice = decisionSource == "user"
		item.IsAutoMatch = domain.MatchDecision(item.Decision) == domain.MatchAuto
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending review items: %w", err)
	}

	// Batch-load candidates for all items in a single query.
	if len(items) > 0 {
		allCandidates, err := h.candidateRepo.GetCandidatesByRun(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("load candidates for run %d: %w", runID, err)
		}
		for i := range items {
			for _, c := range allCandidates[items[i].YouTubeVideoID] {
				items[i].Candidates = append(items[i].Candidates, candidateView{
					Rank:       c.Rank,
					SPTrackID:  c.SPTrackID,
					SPTitle:    c.SPTitle,
					SPArtist:   c.SPArtist,
					SpotifyURL: spotifyTrackURL(c.SPTrackID),
					Confidence: c.Confidence,
				})
			}
		}
	}

	return items, nil
}

// youtubeVideoURL returns the canonical watch URL for a YouTube video ID.
func youtubeVideoURL(videoID string) string {
	if videoID == "" {
		return ""
	}
	return "https://www.youtube.com/watch?v=" + videoID
}

// spotifyTrackURL returns the canonical open URL for a Spotify track ID.
func spotifyTrackURL(trackID string) string {
	if trackID == "" {
		return ""
	}
	return "https://open.spotify.com/track/" + trackID
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
