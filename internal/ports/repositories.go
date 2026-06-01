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
}
