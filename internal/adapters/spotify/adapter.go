package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/ports"
)

const defaultBaseURL = "https://api.spotify.com"

// maxPages caps pagination in GetPlaylistTracks to prevent an infinite loop
// caused by a malformed or adversarial "next" response.
// Spotify allows at most 10 000 tracks at 100 per page → 100 pages.
const maxPages = 100

var _ ports.SpotifyService = (*Adapter)(nil)

type Adapter struct {
	client  *http.Client
	baseURL string
	token   string
}

func NewAdapter(client *http.Client, token string) *Adapter {
	if client == nil {
		client = http.DefaultClient
	}

	return &Adapter{
		client:  client,
		baseURL: defaultBaseURL,
		token:   token,
	}
}

func (a *Adapter) SearchTrack(ctx context.Context, query string) (*domain.TrackMatch, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("spotify adapter is not initialized")
	}
	if a.token == "" {
		return nil, fmt.Errorf("spotify oauth token is empty")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is empty")
	}

	endpoint, err := url.Parse(a.baseURL + "/v1/search")
	if err != nil {
		return nil, fmt.Errorf("build spotify search endpoint: %w", err)
	}

	params := endpoint.Query()
	params.Set("q", query)
	params.Set("type", "track")
	params.Set("limit", "1")
	endpoint.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create spotify search request: %w", err)
	}
	a.setAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request spotify search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("spotify search failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode spotify search response: %w", err)
	}

	if len(payload.Tracks.Items) == 0 {
		return nil, nil
	}

	first := payload.Tracks.Items[0]
	artist := ""
	if len(first.Artists) > 0 {
		artist = first.Artists[0].Name
	}

	return &domain.TrackMatch{
		SPTrackID:  first.ID,
		SPTitle:    first.Name,
		SPArtist:   artist,
		Confidence: 0,
	}, nil
}

func (a *Adapter) SearchTracks(ctx context.Context, query string, limit int) ([]domain.TrackMatchCandidate, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("spotify adapter is not initialized")
	}
	if a.token == "" {
		return nil, fmt.Errorf("spotify oauth token is empty")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is empty")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}

	endpoint, err := url.Parse(a.baseURL + "/v1/search")
	if err != nil {
		return nil, fmt.Errorf("build spotify search endpoint: %w", err)
	}

	params := endpoint.Query()
	params.Set("q", query)
	params.Set("type", "track")
	params.Set("limit", fmt.Sprintf("%d", limit))
	endpoint.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create spotify search request: %w", err)
	}
	a.setAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request spotify search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("spotify search failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode spotify search response: %w", err)
	}

	candidates := make([]domain.TrackMatchCandidate, 0, len(payload.Tracks.Items))
	for i, item := range payload.Tracks.Items {
		artist := ""
		if len(item.Artists) > 0 {
			artist = item.Artists[0].Name
		}
		candidates = append(candidates, domain.TrackMatchCandidate{
			SPTrackID: item.ID,
			SPTitle:   item.Name,
			SPArtist:  artist,
			Rank:      i + 1,
		})
	}

	return candidates, nil
}

func (a *Adapter) GetPlaylistTracks(ctx context.Context, playlistID string) ([]string, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("spotify adapter is not initialized")
	}
	if a.token == "" {
		return nil, fmt.Errorf("spotify oauth token is empty")
	}
	if strings.TrimSpace(playlistID) == "" {
		return nil, fmt.Errorf("playlist id is empty")
	}

	// Parse baseURL once so we can validate every "next" URL against it (SSRF guard).
	baseURL, err := url.Parse(a.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse spotify base url: %w", err)
	}

	endpoint, err := url.Parse(a.baseURL + "/v1/playlists/" + url.PathEscape(playlistID) + "/tracks")
	if err != nil {
		return nil, fmt.Errorf("build spotify playlist tracks endpoint: %w", err)
	}

	params := endpoint.Query()
	params.Set("fields", "items(track(id)),next")
	params.Set("limit", "100")
	endpoint.RawQuery = params.Encode()

	var trackIDs []string
	page := 0
	for {
		// Guard against an infinite loop caused by a response that always
		// returns a non-empty "next" URL.
		if page >= maxPages {
			return nil, fmt.Errorf("GetPlaylistTracks: exceeded max pagination pages (%d) for playlist %s", maxPages, playlistID)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create spotify playlist tracks request: %w", err)
		}
		a.setAuth(req)

		resp, err := a.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request spotify playlist tracks: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("spotify get playlist tracks failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
		}

		var payload playlistTracksResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode spotify playlist tracks response: %w", decodeErr)
		}

		for _, item := range payload.Items {
			id := strings.TrimSpace(item.Track.ID)
			if id == "" {
				continue
			}
			trackIDs = append(trackIDs, id)
		}

		// *string makes absent (JSON null) vs present-but-empty explicit.
		if payload.Next == nil || strings.TrimSpace(*payload.Next) == "" {
			break
		}

		nextEndpoint, err := url.Parse(*payload.Next)
		if err != nil {
			return nil, fmt.Errorf("parse spotify playlist next page url: %w", err)
		}

		// Reject any "next" URL whose host doesn't match the configured base URL
		// to prevent following an attacker-controlled redirect (SSRF).
		if nextEndpoint.Host != baseURL.Host {
			return nil, fmt.Errorf("GetPlaylistTracks: next page url host %q does not match expected %q", nextEndpoint.Host, baseURL.Host)
		}

		endpoint = nextEndpoint
		page++
	}

	return trackIDs, nil
}

func (a *Adapter) AddTrackToPlaylist(ctx context.Context, playlistID, trackID string) error {
	if a == nil || a.client == nil {
		return fmt.Errorf("spotify adapter is not initialized")
	}
	if a.token == "" {
		return fmt.Errorf("spotify oauth token is empty")
	}
	if strings.TrimSpace(playlistID) == "" {
		return fmt.Errorf("playlist id is empty")
	}
	if strings.TrimSpace(trackID) == "" {
		return fmt.Errorf("track id is empty")
	}

	endpoint, err := url.Parse(a.baseURL + "/v1/playlists/" + url.PathEscape(playlistID) + "/tracks")
	if err != nil {
		return fmt.Errorf("build spotify playlist endpoint: %w", err)
	}

	body := strings.NewReader(fmt.Sprintf(`{"uris":["spotify:track:%s"]}`, trackID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create spotify add track request: %w", err)
	}
	a.setAuth(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("request spotify add track: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("spotify add track failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	return nil
}

func (a *Adapter) ListUserPlaylists(ctx context.Context) ([]ports.PlaylistSummary, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("spotify adapter is not initialized")
	}
	if a.token == "" {
		return nil, fmt.Errorf("spotify oauth token is empty")
	}

	baseURL, err := url.Parse(a.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse spotify base url: %w", err)
	}

	endpoint, err := url.Parse(a.baseURL + "/v1/me/playlists")
	if err != nil {
		return nil, fmt.Errorf("build spotify user playlists endpoint: %w", err)
	}

	params := endpoint.Query()
	params.Set("fields", "items(id,name),next")
	params.Set("limit", "50")
	endpoint.RawQuery = params.Encode()

	var summaries []ports.PlaylistSummary
	page := 0
	for {
		if page >= maxPages {
			return nil, fmt.Errorf("ListUserPlaylists: exceeded max pagination pages (%d)", maxPages)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create spotify user playlists request: %w", err)
		}
		a.setAuth(req)

		resp, err := a.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request spotify user playlists: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("spotify list user playlists failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
		}

		var payload userPlaylistsResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode spotify user playlists response: %w", decodeErr)
		}

		for _, item := range payload.Items {
			summaries = append(summaries, ports.PlaylistSummary{
				ID:    item.ID,
				Title: item.Name,
			})
		}

		if payload.Next == nil || strings.TrimSpace(*payload.Next) == "" {
			break
		}

		nextEndpoint, err := url.Parse(*payload.Next)
		if err != nil {
			return nil, fmt.Errorf("parse spotify user playlists next page url: %w", err)
		}

		if nextEndpoint.Host != baseURL.Host {
			return nil, fmt.Errorf("ListUserPlaylists: next page url host %q does not match expected %q", nextEndpoint.Host, baseURL.Host)
		}

		endpoint = nextEndpoint
		page++
	}

	return summaries, nil
}

func (a *Adapter) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
}

type searchResponse struct {
	Tracks struct {
		Items []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
		} `json:"items"`
	} `json:"tracks"`
}

type playlistTracksResponse struct {
	Items []struct {
		Track struct {
			ID string `json:"id"`
		} `json:"track"`
	} `json:"items"`
	Next *string `json:"next"` // pointer: JSON null → nil (absent), vs non-nil empty string (present but empty)
}

type userPlaylistsResponse struct {
	Items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"items"`
	Next *string `json:"next"`
}
