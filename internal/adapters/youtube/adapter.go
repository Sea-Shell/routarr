package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/ports"
)

const (
	defaultBaseURL = "https://www.googleapis.com"
	playlistPath   = "/youtube/v3/playlistItems"
	videosPath     = "/youtube/v3/videos"
)

var _ ports.YouTubeService = (*Adapter)(nil)

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

type playlistVideo struct {
	videoID     string
	title       string
	description string
	duration    string
}

func (a *Adapter) GetPlaylistVideos(ctx context.Context, playlistID string) ([]domain.TrackMatch, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("youtube adapter is not initialized")
	}
	if a.token == "" {
		return nil, fmt.Errorf("youtube oauth token is empty")
	}
	if strings.TrimSpace(playlistID) == "" {
		return nil, fmt.Errorf("playlist id is empty")
	}

	var videos []playlistVideo
	pageToken := ""

	for {
		res, err := a.fetchPlaylistPage(ctx, playlistID, pageToken)
		if err != nil {
			return nil, err
		}

		pageVideos := make([]playlistVideo, 0, len(res.Items))
		videoIDs := make([]string, 0, len(res.Items))

		for _, item := range res.Items {
			videoID := item.Snippet.ResourceID.VideoID
			if videoID == "" {
				videoID = item.ContentDetails.VideoID
			}
			if videoID == "" {
				continue
			}

			video := playlistVideo{
				videoID:     videoID,
				title:       item.Snippet.Title,
				description: item.Snippet.Description,
			}
			pageVideos = append(pageVideos, video)
			videoIDs = append(videoIDs, videoID)
		}

		durations, err := a.fetchVideoDurations(ctx, videoIDs)
		if err != nil {
			return nil, err
		}

		for i := range pageVideos {
			pageVideos[i].duration = durations[pageVideos[i].videoID]
			videos = append(videos, pageVideos[i])
		}

		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}

	matches := make([]domain.TrackMatch, 0, len(videos))
	for _, video := range videos {
		title := strings.TrimSpace(video.title)
		if title == "" {
			title = strings.TrimSpace(video.description)
		}

		matches = append(matches, domain.TrackMatch{
			YTVideoID: video.videoID,
			YTTitle:   title,
		})
	}

	return matches, nil
}

func (a *Adapter) fetchPlaylistPage(ctx context.Context, playlistID, pageToken string) (*playlistItemsResponse, error) {
	endpoint, err := url.Parse(a.baseURL + playlistPath)
	if err != nil {
		return nil, fmt.Errorf("build youtube playlist endpoint: %w", err)
	}

	query := endpoint.Query()
	query.Set("part", "snippet,contentDetails")
	query.Set("playlistId", playlistID)
	query.Set("maxResults", strconv.Itoa(50))
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create youtube playlist request: %w", err)
	}
	a.setAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request youtube playlist items: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("youtube playlist items request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload playlistItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode youtube playlist response: %w", err)
	}

	return &payload, nil
}

func (a *Adapter) fetchVideoDurations(ctx context.Context, videoIDs []string) (map[string]string, error) {
	if len(videoIDs) == 0 {
		return map[string]string{}, nil
	}

	endpoint, err := url.Parse(a.baseURL + videosPath)
	if err != nil {
		return nil, fmt.Errorf("build youtube videos endpoint: %w", err)
	}

	query := endpoint.Query()
	query.Set("part", "contentDetails")
	query.Set("id", strings.Join(videoIDs, ","))
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create youtube videos request: %w", err)
	}
	a.setAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request youtube videos: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("youtube videos request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload videosResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode youtube videos response: %w", err)
	}

	durations := make(map[string]string, len(payload.Items))
	for _, item := range payload.Items {
		durations[item.ID] = item.ContentDetails.Duration
	}

	return durations, nil
}

func (a *Adapter) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
}

type playlistItemsResponse struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []struct {
		Snippet struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			ResourceID  struct {
				VideoID string `json:"videoId"`
			} `json:"resourceId"`
		} `json:"snippet"`
		ContentDetails struct {
			VideoID string `json:"videoId"`
		} `json:"contentDetails"`
	} `json:"items"`
}

type videosResponse struct {
	Items []struct {
		ID             string `json:"id"`
		ContentDetails struct {
			Duration string `json:"duration"`
		} `json:"contentDetails"`
	} `json:"items"`
}
