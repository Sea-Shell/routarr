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
