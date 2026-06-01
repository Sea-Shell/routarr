package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/ports"
)

const syncRunStatusPending = "pending"

const (
	syncRunStatusCompleted        = "completed"
	syncItemActionAdded           = "added"
	syncItemActionSkippedDuplicate = "skipped_duplicate"
	syncItemActionFailed          = "failed"
)

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
	youtubeService ports.YouTubeService
	spotifyService ports.SpotifyService
	mappingRepo    ports.MappingRepository
	matchRepo      ports.MatchRepository
	matcher        Matcher
	syncRunRepo    syncRunRepository
}

func NewSyncService(
	youtubeService ports.YouTubeService,
	spotifyService ports.SpotifyService,
	mappingRepo ports.MappingRepository,
	matchRepo ports.MatchRepository,
	matcher Matcher,
) *SyncService {
	var syncRunRepo syncRunRepository
	if repo, ok := any(mappingRepo).(syncRunRepository); ok {
		syncRunRepo = repo
	}

	return &SyncService{
		youtubeService: youtubeService,
		spotifyService: spotifyService,
		mappingRepo:    mappingRepo,
		matchRepo:      matchRepo,
		matcher:        matcher,
		syncRunRepo:    syncRunRepo,
	}
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

	videos, err := s.youtubeService.GetPlaylistVideos(ctx, mapping.YTPlaylistID)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch youtube playlist %q: %w", mapping.YTPlaylistID, err)
	}

	matches := make([]domain.TrackMatch, 0, len(videos))
	for _, video := range videos {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		match, err := s.resolveMatch(ctx, video)
		if err != nil {
			return nil, nil, err
		}
		if match == nil {
			continue
		}

		matches = append(matches, *match)
	}

	return run, matches, nil
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

func (s *SyncService) resolveMatch(ctx context.Context, video domain.TrackMatch) (*domain.TrackMatch, error) {
	existing, err := s.matchRepo.GetMatch(ctx, video.YTVideoID)
	if err != nil {
		return nil, fmt.Errorf("get previous match for %q: %w", video.YTVideoID, err)
	}
	if existing != nil {
		if existing.Decision == domain.MatchRejected {
			return nil, nil
		}
		return existing, nil
	}

	query := s.matcher.Normalize(video.YTTitle)
	if query == "" {
		query = video.YTTitle
	}

	candidate, err := s.spotifyService.SearchTrack(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search spotify track for video %q: %w", video.YTVideoID, err)
	}

	resolved := domain.TrackMatch{
		YTVideoID: video.YTVideoID,
		YTTitle:   video.YTTitle,
		Decision:  domain.MatchRejected,
	}

	if candidate != nil {
		score := s.matcher.Score(video.YTTitle, candidate.SPTitle, candidate.SPArtist)
		decision := s.matcher.Classify(score)

		resolved.SPTrackID = candidate.SPTrackID
		resolved.SPTitle = candidate.SPTitle
		resolved.SPArtist = candidate.SPArtist
		resolved.Confidence = score
		resolved.Decision = decision
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
