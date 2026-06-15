package ports

import (
	"context"
	"time"

	"github.com/bateau84/routarr/internal/domain"
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
	// UpdateSyncedAt records that a track was successfully added to the Spotify playlist.
	UpdateSyncedAt(ctx context.Context, ytVideoID string, syncedAt time.Time) error
	// UpdateResyncRequestedAt records that a user requested a re-sync for this track.
	UpdateResyncRequestedAt(ctx context.Context, ytVideoID string, resyncAt time.Time) error
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

// SettingsRepository persists user preferences.
type SettingsRepository interface {
	Load(ctx context.Context) (*domain.UserSettings, error)
	Save(ctx context.Context, s *domain.UserSettings) error
}

