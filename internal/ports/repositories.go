package ports

import (
	"context"

	"github.com/bateau84/yt2sp/internal/domain"
)

type MappingRepository interface {
	Save(ctx context.Context, m *domain.PlaylistMapping) error
	GetByID(ctx context.Context, id int) (*domain.PlaylistMapping, error)
	List(ctx context.Context) ([]domain.PlaylistMapping, error)
}

type MatchRepository interface {
	SaveMatch(ctx context.Context, m *domain.TrackMatch) error
	GetMatch(ctx context.Context, ytVideoID string) (*domain.TrackMatch, error)
	// UpdateMatchChoice persists a user-selected Spotify track as a global manual decision.
	UpdateMatchChoice(ctx context.Context, ytVideoID, spTrackID, spTitle, spArtist string, decision domain.MatchDecision) error
}

// CandidateRepository persists Spotify search candidates per sync run.
type CandidateRepository interface {
	SaveCandidates(ctx context.Context, candidates []domain.TrackMatchCandidate) error
	GetCandidates(ctx context.Context, syncRunID int, ytVideoID string) ([]domain.TrackMatchCandidate, error)
	// GetCandidatesByRun loads all candidates for a sync run in one query, keyed by YTVideoID.
	GetCandidatesByRun(ctx context.Context, syncRunID int) (map[string][]domain.TrackMatchCandidate, error)
}

// SyncRunEventRepository persists structured log events for a sync run.
type SyncRunEventRepository interface {
	SaveSyncRunEvent(ctx context.Context, event *domain.SyncRunEvent) error
	ListSyncRunEvents(ctx context.Context, runID int) ([]domain.SyncRunEvent, error)
}

