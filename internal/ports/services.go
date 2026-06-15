package ports

import (
	"context"

	"github.com/bateau84/routarr/internal/domain"
)

type PlaylistSummary struct {
	ID    string
	Title string
}

type YouTubeService interface {
	GetPlaylistVideos(ctx context.Context, playlistID string) ([]domain.TrackMatch, error)
	ListUserPlaylists(ctx context.Context) ([]PlaylistSummary, error)
}

type SpotifyService interface {
	SearchTrack(ctx context.Context, query string) (*domain.TrackMatch, error)
	// SearchTracks returns up to limit Spotify candidates for the query.
	// Results are ranked 1..n by their position in the Spotify search response.
	SearchTracks(ctx context.Context, query string, limit int) ([]domain.TrackMatchCandidate, error)
	GetPlaylistTracks(ctx context.Context, playlistID string) ([]string, error)
	AddTrackToPlaylist(ctx context.Context, playlistID, trackID string) error
	ListUserPlaylists(ctx context.Context) ([]PlaylistSummary, error)
}
