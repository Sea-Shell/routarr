package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetPlaylistVideos_PaginatesAndReturnsTracks(t *testing.T) {
	t.Parallel()

	type videoMeta struct {
		Title       string
		Description string
		Duration    string
	}

	videos := map[string]videoMeta{
		"v1": {Title: "Track One", Description: "desc1", Duration: "PT3M30S"},
		"v2": {Title: "Track Two", Description: "desc2", Duration: "PT4M10S"},
		"v3": {Title: "", Description: "fallback title from description", Duration: "PT2M59S"},
	}

	playlistPages := map[string][]string{
		"":      {"v1", "v2"},
		"page-2": {"v3"},
	}

	var playlistCalls int
	var videosCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-yt" {
			t.Fatalf("authorization header = %q, want %q", got, "Bearer token-yt")
		}

		switch r.URL.Path {
		case playlistPath:
			playlistCalls++
			if got := r.URL.Query().Get("maxResults"); got != "50" {
				t.Fatalf("maxResults = %q, want 50", got)
			}
			if got := r.URL.Query().Get("part"); got != "snippet,contentDetails" {
				t.Fatalf("part = %q, want snippet,contentDetails", got)
			}

			pageToken := r.URL.Query().Get("pageToken")
			ids, ok := playlistPages[pageToken]
			if !ok {
				t.Fatalf("unexpected page token %q", pageToken)
			}

			next := ""
			if pageToken == "" {
				next = "page-2"
			}

			items := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				meta := videos[id]
				items = append(items, map[string]any{
					"snippet": map[string]any{
						"title":       meta.Title,
						"description": meta.Description,
						"resourceId": map[string]any{
							"videoId": id,
						},
					},
				})
			}

			payload := map[string]any{"items": items, "nextPageToken": next}
			_ = json.NewEncoder(w).Encode(payload)

		case videosPath:
			videosCalls++
			if got := r.URL.Query().Get("part"); got != "contentDetails,status" {
				t.Fatalf("videos part = %q, want contentDetails,status", got)
			}

			requestedIDs := strings.Split(r.URL.Query().Get("id"), ",")
			items := make([]map[string]any, 0, len(requestedIDs))
			for _, id := range requestedIDs {
				meta, ok := videos[id]
				if !ok {
					t.Fatalf("unexpected video id requested: %q", id)
				}
				items = append(items, map[string]any{
					"id": id,
					"status": map[string]any{
						"privacyStatus": "public",
					},
					"contentDetails": map[string]any{
						"duration": meta.Duration,
					},
				})
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})

		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), "token-yt")
	adapter.baseURL = server.URL

	got, err := adapter.GetPlaylistVideos(context.Background(), "playlist-123")
	if err != nil {
		t.Fatalf("GetPlaylistVideos() error = %v", err)
	}

	if playlistCalls != 2 {
		t.Fatalf("playlist calls = %d, want 2", playlistCalls)
	}
	if videosCalls != 2 {
		t.Fatalf("videos calls = %d, want 2", videosCalls)
	}

	if len(got) != 3 {
		t.Fatalf("len(tracks) = %d, want 3", len(got))
	}

	if got[0].YTVideoID != "v1" || got[0].YTTitle != "Track One" {
		t.Fatalf("first track mismatch: %+v", got[0])
	}
	if got[1].YTVideoID != "v2" || got[1].YTTitle != "Track Two" {
		t.Fatalf("second track mismatch: %+v", got[1])
	}
	if got[2].YTVideoID != "v3" || got[2].YTTitle != "fallback title from description" {
		t.Fatalf("third track mismatch (fallback expected): %+v", got[2])
	}
}

func TestGetPlaylistVideos_ValidationAndFailures(t *testing.T) {
	t.Parallel()

	t.Run("empty token", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "")
		_, err := adapter.GetPlaylistVideos(context.Background(), "playlist")
		if err == nil || !strings.Contains(err.Error(), "oauth token") {
			t.Fatalf("expected oauth token error, got %v", err)
		}
	})

	t.Run("empty playlist id", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "token")
		_, err := adapter.GetPlaylistVideos(context.Background(), " ")
		if err == nil || !strings.Contains(err.Error(), "playlist id is empty") {
			t.Fatalf("expected playlist id error, got %v", err)
		}
	})

	t.Run("non-200 from playlist endpoint", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == playlistPath {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}))
		defer server.Close()

		adapter := NewAdapter(server.Client(), "token")
		adapter.baseURL = server.URL

		_, err := adapter.GetPlaylistVideos(context.Background(), "playlist")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status %d", http.StatusBadGateway)) {
			t.Fatalf("expected status error, got %v", err)
		}
	})
}

// TestGetPlaylistVideos_SkipsUnavailableVideos verifies that private videos (by status),
// videos with the title "Private video", and videos with the title "Deleted video" are
// all excluded from the returned track list.
func TestGetPlaylistVideos_SkipsUnavailableVideos(t *testing.T) {
	t.Parallel()

	type videoMeta struct {
		Title         string
		Duration      string
		PrivacyStatus string // "public", "unlisted", "private"
	}

	// v1 is a normal public video — should be returned.
	// v2 has privacyStatus="private" from the API — should be skipped.
	// v3 has title "Private video" (snippet-level indicator) — should be skipped.
	// v4 has title "Deleted video" — should be skipped.
	// v5 is unlisted — should be returned (still accessible via direct link).
	videos := map[string]videoMeta{
		"v1": {Title: "Good Track", Duration: "PT3M00S", PrivacyStatus: "public"},
		"v2": {Title: "Some Title", Duration: "PT3M00S", PrivacyStatus: "private"},
		"v3": {Title: "Private video", Duration: "", PrivacyStatus: "public"},
		"v4": {Title: "Deleted video", Duration: "", PrivacyStatus: "public"},
		"v5": {Title: "Unlisted Track", Duration: "PT2M00S", PrivacyStatus: "unlisted"},
	}

	allIDs := []string{"v1", "v2", "v3", "v4", "v5"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case playlistPath:
			items := make([]map[string]any, 0, len(allIDs))
			for _, id := range allIDs {
				meta := videos[id]
				items = append(items, map[string]any{
					"snippet": map[string]any{
						"title": meta.Title,
						"resourceId": map[string]any{
							"videoId": id,
						},
					},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextPageToken": ""})

		case videosPath:
			requestedIDs := strings.Split(r.URL.Query().Get("id"), ",")
			items := make([]map[string]any, 0, len(requestedIDs))
			for _, id := range requestedIDs {
				meta := videos[id]
				items = append(items, map[string]any{
					"id": id,
					"status": map[string]any{
						"privacyStatus": meta.PrivacyStatus,
					},
					"contentDetails": map[string]any{
						"duration": meta.Duration,
					},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})

		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), "token-yt")
	adapter.baseURL = server.URL

	got, err := adapter.GetPlaylistVideos(context.Background(), "playlist-abc")
	if err != nil {
		t.Fatalf("GetPlaylistVideos() error = %v", err)
	}

	// Only v1 and v5 should survive.
	if len(got) != 2 {
		t.Fatalf("len(tracks) = %d, want 2; tracks = %+v", len(got), got)
	}

	wantIDs := []string{"v1", "v5"}
	for i, want := range wantIDs {
		if got[i].YTVideoID != want {
			t.Fatalf("track[%d].YTVideoID = %q, want %q", i, got[i].YTVideoID, want)
		}
	}
}

// TestGetPlaylistVideos_SkipsVideoAbsentFromVideosAPI verifies that a video ID
// returned by the playlist API but entirely absent from the Videos API response
// (e.g. hard-deleted content) is treated as unavailable and skipped.
func TestGetPlaylistVideos_SkipsVideoAbsentFromVideosAPI(t *testing.T) {
	t.Parallel()

	// v1 is present and available in the Videos API response.
	// v2 appears in the playlist but the Videos API silently omits it (hard-deleted).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case playlistPath:
			items := []map[string]any{
				{"snippet": map[string]any{"title": "Good Track", "resourceId": map[string]any{"videoId": "v1"}}},
				{"snippet": map[string]any{"title": "Deleted video", "resourceId": map[string]any{"videoId": "v2"}}},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextPageToken": ""})

		case videosPath:
			// Only v1 is returned; v2 is absent (hard-deleted from YouTube).
			items := []map[string]any{
				{
					"id":             "v1",
					"status":         map[string]any{"privacyStatus": "public"},
					"contentDetails": map[string]any{"duration": "PT3M00S"},
				},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})

		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), "token-yt")
	adapter.baseURL = server.URL

	got, err := adapter.GetPlaylistVideos(context.Background(), "playlist-xyz")
	if err != nil {
		t.Fatalf("GetPlaylistVideos() error = %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("len(tracks) = %d, want 1; tracks = %+v", len(got), got)
	}
	if got[0].YTVideoID != "v1" {
		t.Fatalf("track[0].YTVideoID = %q, want v1", got[0].YTVideoID)
	}
}
