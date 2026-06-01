package app

import (
	"context"
	"fmt"
	"time"

	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/ports"
)

const syncRunStatusPending = "pending"

type Matcher interface {
	Normalize(title string) string
	Score(videoTitle, candidateTitle, artist string) float64
	Classify(score float64) domain.MatchDecision
}

type syncRunRepository interface {
	SaveSyncRun(ctx context.Context, run *domain.SyncRun) error
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
