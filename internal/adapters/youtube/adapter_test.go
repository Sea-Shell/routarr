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
			if got := r.URL.Query().Get("part"); got != "contentDetails" {
				t.Fatalf("videos part = %q, want contentDetails", got)
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
