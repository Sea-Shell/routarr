package ports

import (
	"context"

	"github.com/bateau84/yt2sp/internal/domain"
)

type YouTubeService interface {
	GetPlaylistVideos(ctx context.Context, playlistID string) ([]domain.TrackMatch, error)
}

type SpotifyService interface {
	SearchTrack(ctx context.Context, query string) (*domain.TrackMatch, error)
	GetPlaylistTracks(ctx context.Context, playlistID string) ([]string, error)
	AddTrackToPlaylist(ctx context.Context, playlistID, trackID string) error
}
