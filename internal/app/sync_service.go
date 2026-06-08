package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/ports"
)

const syncRunStatusPending = "pending"

const (
	syncRunStatusRunning           = "running"
	syncRunStatusCompleted         = "completed"
	syncRunStatusFailed            = "failed"
	syncItemActionAdded            = "added"
	syncItemActionSkippedDuplicate = "skipped_duplicate"
	syncItemActionFailed           = "failed"
)

// maxCandidates is the number of Spotify search results to fetch and store
// as alternatives per YouTube video during a dry run.
const maxCandidates = 5

var ErrSyncRunNotFound = errors.New("sync run not found")

type Matcher interface {
	Normalize(title string) string
	Score(videoTitle, candidateTitle, artist string) float64
	Classify(score float64) domain.MatchDecision
}

type syncRunRepository interface {
	SaveSyncRun(ctx context.Context, run *domain.SyncRun) error
	GetSyncRunByID(ctx context.Context, runID int) (*domain.SyncRun, error)
	ListSyncRunMatches(ctx context.Context, runID int) ([]domain.TrackMatch, error)
	UpdateSyncItemAction(ctx context.Context, runID int, ytVideoID, action, itemError string) error
	UpdateSyncRunStatus(ctx context.Context, runID int, status string, finishedAt time.Time) error
}

type SyncService struct {
	youtubeService   ports.YouTubeService
	spotifyService   ports.SpotifyService
	mappingRepo      ports.MappingRepository
	matchRepo        ports.MatchRepository
	candidateRepo    ports.CandidateRepository
	matcher          Matcher
	syncRunRepo      syncRunRepository
	progressReporter ports.ProgressReporter
}

func NewSyncService(
	youtubeService ports.YouTubeService,
	spotifyService ports.SpotifyService,
	mappingRepo ports.MappingRepository,
	matchRepo ports.MatchRepository,
	matcher Matcher,
	opts ...SyncServiceOption,
) *SyncService {
	var syncRunRepo syncRunRepository
	if repo, ok := any(mappingRepo).(syncRunRepository); ok {
		syncRunRepo = repo
	}

	svc := &SyncService{
		youtubeService: youtubeService,
		spotifyService: spotifyService,
		mappingRepo:    mappingRepo,
		matchRepo:      matchRepo,
		matcher:        matcher,
		syncRunRepo:    syncRunRepo,
	}

	for _, opt := range opts {
		opt(svc)
	}

	return svc
}

// SyncServiceOption allows optional configuration of SyncService.
type SyncServiceOption func(*SyncService)

// WithCandidateRepository attaches a CandidateRepository so dry runs persist
// top-N Spotify alternatives for review.
func WithCandidateRepository(repo ports.CandidateRepository) SyncServiceOption {
	return func(s *SyncService) {
		s.candidateRepo = repo
	}
}

// WithProgressReporter attaches a ProgressReporter that receives structured
// progress notifications during sync operations. If nil or not provided, no
// progress is reported. Reporter is called fire-and-forget; errors are ignored
// so that reporting failures cannot abort a sync.
func WithProgressReporter(reporter ports.ProgressReporter) SyncServiceOption {
	return func(s *SyncService) {
		s.progressReporter = reporter
	}
}

// reportProgress emits a progress event if a reporter is configured.
// It is a no-op when s.progressReporter is nil.
func (s *SyncService) reportProgress(ctx context.Context, runID int, level, message string) {
	if s.progressReporter == nil {
		return
	}
	s.progressReporter.Report(ctx, runID, level, message)
}

func (s *SyncService) RunDry(ctx context.Context, mappingID int) (*domain.SyncRun, []domain.TrackMatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if mappingID <= 0 {
		return nil, nil, fmt.Errorf("mapping id must be positive")
	}

	mapping, err := s.mappingRepo.GetByID(ctx, mappingID)
	if err != nil {
		return nil, nil, fmt.Errorf("get mapping by id %d: %w", mappingID, err)
	}
	if mapping == nil {
		return nil, nil, fmt.Errorf("mapping id %d not found", mappingID)
	}

	run := &domain.SyncRun{
		MappingID: mapping.ID,
		StartedAt: time.Now().UTC(),
		Status:    syncRunStatusPending,
	}
	if err := s.saveSyncRun(ctx, run); err != nil {
		return nil, nil, err
	}

	s.reportProgress(ctx, run.ID, "info", "Created dry sync run")
	s.reportProgress(ctx, run.ID, "info", "Fetching YouTube playlist")

	videos, err := s.youtubeService.GetPlaylistVideos(ctx, mapping.YTPlaylistID)
	if err != nil {
		s.reportProgress(ctx, run.ID, "error", fmt.Sprintf("Failed to fetch YouTube playlist %q: %v", mapping.YTPlaylistID, err))
		return nil, nil, fmt.Errorf("fetch youtube playlist %q: %w", mapping.YTPlaylistID, err)
	}

	s.reportProgress(ctx, run.ID, "info", fmt.Sprintf("Fetched %d YouTube videos", len(videos)))

	matches := make([]domain.TrackMatch, 0, len(videos))
	total := len(videos)
	for i, video := range videos {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		s.reportProgress(ctx, run.ID, "info", fmt.Sprintf("Matching video %d/%d: %s", i+1, total, video.YTTitle))

		match, err := s.resolveMatch(ctx, run.ID, video)
		if err != nil {
			s.reportProgress(ctx, run.ID, "error", fmt.Sprintf("Failed to match video %q: %v", video.YTVideoID, err))
			return nil, nil, err
		}
		if match == nil {
			continue
		}

		matches = append(matches, *match)
	}

	s.reportProgress(ctx, run.ID, "success", "Finished matching")

	return run, matches, nil
}

// RunDryInto performs the same work as RunDry but uses a pre-created run
// (with a valid ID already set) instead of creating one internally. The caller
// is responsible for creating and persisting the run before calling this method.
// This enables the web handler to redirect immediately to the progress page
// before the dry sync completes.
func (s *SyncService) RunDryInto(ctx context.Context, run *domain.SyncRun, mappingID int) ([]domain.TrackMatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mappingID <= 0 {
		return nil, fmt.Errorf("mapping id must be positive")
	}
	if run == nil || run.ID <= 0 {
		return nil, fmt.Errorf("pre-created run must have a valid ID")
	}

	mapping, err := s.mappingRepo.GetByID(ctx, mappingID)
	if err != nil {
		return nil, fmt.Errorf("get mapping by id %d: %w", mappingID, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("mapping id %d not found", mappingID)
	}

	s.reportProgress(ctx, run.ID, "info", "Fetching YouTube playlist")

	videos, err := s.youtubeService.GetPlaylistVideos(ctx, mapping.YTPlaylistID)
	if err != nil {
		s.reportProgress(ctx, run.ID, "error", fmt.Sprintf("Failed to fetch YouTube playlist %q: %v", mapping.YTPlaylistID, err))
		return nil, fmt.Errorf("fetch youtube playlist %q: %w", mapping.YTPlaylistID, err)
	}

	s.reportProgress(ctx, run.ID, "info", fmt.Sprintf("Fetched %d YouTube videos", len(videos)))

	matches := make([]domain.TrackMatch, 0, len(videos))
	total := len(videos)
	for i, video := range videos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		s.reportProgress(ctx, run.ID, "info", fmt.Sprintf("Matching video %d/%d: %s", i+1, total, video.YTTitle))

		match, err := s.resolveMatch(ctx, run.ID, video)
		if err != nil {
			s.reportProgress(ctx, run.ID, "error", fmt.Sprintf("Failed to match video %q: %v", video.YTVideoID, err))
			return nil, err
		}
		if match == nil {
			continue
		}

		matches = append(matches, *match)
	}

	s.reportProgress(ctx, run.ID, "success", "Finished matching")

	return matches, nil
}

func (s *SyncService) saveSyncRun(ctx context.Context, run *domain.SyncRun) error {
	if s.syncRunRepo == nil {
		return nil
	}

	if err := s.syncRunRepo.SaveSyncRun(ctx, run); err != nil {
		return fmt.Errorf("save sync run: %w", err)
	}

	return nil
}

func (s *SyncService) resolveMatch(ctx context.Context, runID int, video domain.TrackMatch) (*domain.TrackMatch, error) {
	existing, err := s.matchRepo.GetMatch(ctx, video.YTVideoID)
	if err != nil {
		return nil, fmt.Errorf("get previous match for %q: %w", video.YTVideoID, err)
	}
	if existing != nil {
		if existing.Decision == domain.MatchRejected {
			return nil, nil
		}
		// Mark as prior choice only for saved manual decisions (decision_source="user").
		// Auto-approved matches from prior runs should not show the "Using previous choice" badge.
		existing.IsPriorChoice = existing.DecisionSource == "user"
		return existing, nil
	}

	query := s.matcher.Normalize(video.YTTitle)
	if query == "" {
		query = video.YTTitle
	}

	// Fetch top-N candidates from Spotify for review.
	rawCandidates, err := s.spotifyService.SearchTracks(ctx, query, maxCandidates)
	if err != nil {
		return nil, fmt.Errorf("search spotify tracks for video %q: %w", video.YTVideoID, err)
	}

	// Score all candidates and find the best.
	bestIdx := -1
	bestScore := -1.0

	scoredCandidates := make([]domain.TrackMatchCandidate, 0, len(rawCandidates))
	for i := range rawCandidates {
		c := rawCandidates[i]
		c.SyncRunID = runID
		c.YTVideoID = video.YTVideoID
		score := s.matcher.Score(video.YTTitle, c.SPTitle, c.SPArtist)
		c.Confidence = score
		scoredCandidates = append(scoredCandidates, c)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	// Persist candidates for the review UI (best-effort; log on failure).
	if s.candidateRepo != nil && len(scoredCandidates) > 0 {
		if saveErr := s.candidateRepo.SaveCandidates(ctx, scoredCandidates); saveErr != nil {
			slog.Warn("failed to save candidates for review", "video_id", video.YTVideoID, "error", saveErr)
		}
	}

	resolved := domain.TrackMatch{
		YTVideoID:  video.YTVideoID,
		YTTitle:    video.YTTitle,
		Decision:   domain.MatchRejected,
		Candidates: scoredCandidates,
	}

	if bestIdx >= 0 {
		best := scoredCandidates[bestIdx]
		decision := s.matcher.Classify(bestScore)

		resolved.SPTrackID  = best.SPTrackID
		resolved.SPTitle     = best.SPTitle
		resolved.SPArtist    = best.SPArtist
		resolved.Confidence  = bestScore
		resolved.Decision    = decision
	}

	if err := s.matchRepo.SaveMatch(ctx, &resolved); err != nil {
		return nil, fmt.Errorf("save match for video %q: %w", video.YTVideoID, err)
	}

	if resolved.Decision == domain.MatchAuto || resolved.Decision == domain.MatchPending {
		return &resolved, nil
	}

	return nil, nil
}

func (s *SyncService) Commit(ctx context.Context, syncRunID int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if syncRunID <= 0 {
		return fmt.Errorf("sync run id must be positive")
	}
	if s.syncRunRepo == nil {
		return fmt.Errorf("sync run repository is not configured")
	}
	if s.spotifyService == nil {
		return fmt.Errorf("spotify service is not configured")
	}

	run, err := s.syncRunRepo.GetSyncRunByID(ctx, syncRunID)
	if err != nil {
		return fmt.Errorf("get sync run by id %d: %w", syncRunID, err)
	}
	if run == nil {
		return fmt.Errorf("sync run id %d: %w", syncRunID, ErrSyncRunNotFound)
	}

	mapping, err := s.mappingRepo.GetByID(ctx, run.MappingID)
	if err != nil {
		return fmt.Errorf("get mapping by id %d: %w", run.MappingID, err)
	}
	if mapping == nil {
		return fmt.Errorf("mapping id %d not found", run.MappingID)
	}

	items, err := s.syncRunRepo.ListSyncRunMatches(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("list sync run matches for run %d: %w", run.ID, err)
	}

	existingTrackIDs, err := s.spotifyService.GetPlaylistTracks(ctx, mapping.SPPlaylistID)
	if err != nil {
		return fmt.Errorf("get spotify playlist tracks %q: %w", mapping.SPPlaylistID, err)
	}

	existing := make(map[string]struct{}, len(existingTrackIDs))
	for _, trackID := range existingTrackIDs {
		if trackID == "" {
			continue
		}
		existing[trackID] = struct{}{}
	}

	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Decision != domain.MatchAuto && item.Decision != domain.MatchApproved {
			continue
		}

		trackID := strings.TrimSpace(item.SPTrackID)
		if trackID == "" {
			if updateErr := s.syncRunRepo.UpdateSyncItemAction(ctx, run.ID, item.YTVideoID, syncItemActionFailed, "spotify track id is empty"); updateErr != nil {
				return fmt.Errorf("mark sync item %q failed: %w", item.YTVideoID, updateErr)
			}
			continue
		}

		if _, isDuplicate := existing[trackID]; isDuplicate {
			// Track is already in the playlist — record as skipped_duplicate so the UI
			// can distinguish genuine new additions from no-ops (telemetry / summaries).
			if updateErr := s.syncRunRepo.UpdateSyncItemAction(ctx, run.ID, item.YTVideoID, syncItemActionSkippedDuplicate, ""); updateErr != nil {
				return fmt.Errorf("mark sync item %q skipped duplicate: %w", item.YTVideoID, updateErr)
			}
			continue
		}

		if addErr := s.spotifyService.AddTrackToPlaylist(ctx, mapping.SPPlaylistID, trackID); addErr != nil {
			if updateErr := s.syncRunRepo.UpdateSyncItemAction(ctx, run.ID, item.YTVideoID, syncItemActionFailed, addErr.Error()); updateErr != nil {
				return fmt.Errorf("mark sync item %q failed: %w", item.YTVideoID, updateErr)
			}
			continue
		}

		existing[trackID] = struct{}{}
		if updateErr := s.syncRunRepo.UpdateSyncItemAction(ctx, run.ID, item.YTVideoID, syncItemActionAdded, ""); updateErr != nil {
			return fmt.Errorf("mark sync item %q added: %w", item.YTVideoID, updateErr)
		}
	}

	// status=completed reflects that the commit loop finished, not that every item succeeded.
	// Callers must inspect individual item actions to determine per-item results.
	if err := s.syncRunRepo.UpdateSyncRunStatus(ctx, run.ID, syncRunStatusCompleted, time.Now().UTC()); err != nil {
		return fmt.Errorf("update sync run %d status: %w", run.ID, err)
	}

	return nil
}

