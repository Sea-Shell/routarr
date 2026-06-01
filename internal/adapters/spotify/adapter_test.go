package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchTrack_ReturnsFirstCandidate(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" {
			t.Fatalf("path = %s, want /v1/search", r.URL.Path)
		}
		if got := r.URL.Query().Get("type"); got != "track" {
			t.Fatalf("type = %q, want track", got)
		}
		if got := r.URL.Query().Get("q"); got != "daft punk harder better" {
			t.Fatalf("query = %q, want %q", got, "daft punk harder better")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-sp" {
			t.Fatalf("authorization header = %q, want Bearer token-sp", got)
		}

		payload := map[string]any{
			"tracks": map[string]any{
				"items": []map[string]any{
					{
						"id":   "track-1",
						"name": "Harder, Better, Faster, Stronger",
						"artists": []map[string]any{
							{"name": "Daft Punk"},
						},
					},
					{
						"id":   "track-2",
						"name": "Something Else",
					},
				},
			},
		}

		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), "token-sp")
	adapter.baseURL = server.URL

	match, err := adapter.SearchTrack(context.Background(), "daft punk harder better")
	if err != nil {
		t.Fatalf("SearchTrack() error = %v", err)
	}
	if match == nil {
		t.Fatalf("SearchTrack() = nil, want non-nil")
	}

	if match.SPTrackID != "track-1" || match.SPTitle != "Harder, Better, Faster, Stronger" || match.SPArtist != "Daft Punk" {
		t.Fatalf("unexpected match: %+v", match)
	}
	if match.Confidence != 0 {
		t.Fatalf("confidence = %v, want 0", match.Confidence)
	}
}

func TestSearchTrack_NoResultReturnsNil(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tracks": map[string]any{"items": []any{}},
		})
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), "token-sp")
	adapter.baseURL = server.URL

	match, err := adapter.SearchTrack(context.Background(), "unknown song")
	if err != nil {
		t.Fatalf("SearchTrack() error = %v", err)
	}
	if match != nil {
		t.Fatalf("SearchTrack() = %+v, want nil", match)
	}
}

func TestSearchTrack_ValidationAndFailures(t *testing.T) {
	t.Parallel()

	t.Run("empty token", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "")
		_, err := adapter.SearchTrack(context.Background(), "abc")
		if err == nil || !strings.Contains(err.Error(), "oauth token") {
			t.Fatalf("expected oauth token error, got %v", err)
		}
	})

	t.Run("empty query", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "token")
		_, err := adapter.SearchTrack(context.Background(), " ")
		if err == nil || !strings.Contains(err.Error(), "query is empty") {
			t.Fatalf("expected query error, got %v", err)
		}
	})

	t.Run("non-200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "fail", http.StatusInternalServerError)
		}))
		defer server.Close()

		adapter := NewAdapter(server.Client(), "token")
		adapter.baseURL = server.URL

		_, err := adapter.SearchTrack(context.Background(), "abc")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status %d", http.StatusInternalServerError)) {
			t.Fatalf("expected status error, got %v", err)
		}
	})
}

func TestAddTrackToPlaylist_SendsExpectedPayload(t *testing.T) {
	t.Parallel()

	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/playlists/pl-123/tracks" {
			t.Fatalf("path = %s, want /v1/playlists/pl-123/tracks", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-sp" {
			t.Fatalf("authorization header = %q, want Bearer token-sp", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		requestBody = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), "token-sp")
	adapter.baseURL = server.URL

	err := adapter.AddTrackToPlaylist(context.Background(), "pl-123", "trk-987")
	if err != nil {
		t.Fatalf("AddTrackToPlaylist() error = %v", err)
	}

	if requestBody != `{"uris":["spotify:track:trk-987"]}` {
		t.Fatalf("unexpected request body: %s", requestBody)
	}
}

func TestAddTrackToPlaylist_ValidationAndFailures(t *testing.T) {
	t.Parallel()

	t.Run("empty token", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "")
		err := adapter.AddTrackToPlaylist(context.Background(), "pl", "trk")
		if err == nil || !strings.Contains(err.Error(), "oauth token") {
			t.Fatalf("expected oauth token error, got %v", err)
		}
	})

	t.Run("empty playlist id", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "token")
		err := adapter.AddTrackToPlaylist(context.Background(), "", "trk")
		if err == nil || !strings.Contains(err.Error(), "playlist id is empty") {
			t.Fatalf("expected playlist id error, got %v", err)
		}
	})

	t.Run("empty track id", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "token")
		err := adapter.AddTrackToPlaylist(context.Background(), "pl", "")
		if err == nil || !strings.Contains(err.Error(), "track id is empty") {
			t.Fatalf("expected track id error, got %v", err)
		}
	})

	t.Run("non-201", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusBadRequest)
		}))
		defer server.Close()

		adapter := NewAdapter(server.Client(), "token")
		adapter.baseURL = server.URL

		err := adapter.AddTrackToPlaylist(context.Background(), "pl", "trk")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status %d", http.StatusBadRequest)) {
			t.Fatalf("expected status error, got %v", err)
		}
	})
}

func TestGetPlaylistTracks_ReturnsTrackIDsAcrossPages(t *testing.T) {
	t.Parallel()

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-sp" {
			t.Fatalf("authorization header = %q, want Bearer token-sp", got)
		}

		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/playlists/pl-123/tracks"):
			if r.URL.Query().Get("limit") != "100" {
				t.Fatalf("limit = %q, want 100", r.URL.Query().Get("limit"))
			}
			if r.URL.Query().Get("fields") != "items(track(id)),next" {
				t.Fatalf("fields = %q, want items(track(id)),next", r.URL.Query().Get("fields"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"track": map[string]any{"id": "trk-1"}},
					{"track": map[string]any{"id": "trk-2"}},
				},
				"next": serverURL + "/next-page",
			})
		case r.URL.Path == "/next-page":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"track": map[string]any{"id": "trk-3"}}},
				"next":  nil,
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	adapter := NewAdapter(server.Client(), "token-sp")
	adapter.baseURL = server.URL

	trackIDs, err := adapter.GetPlaylistTracks(context.Background(), "pl-123")
	if err != nil {
		t.Fatalf("GetPlaylistTracks() error = %v", err)
	}

	if len(trackIDs) != 3 {
		t.Fatalf("GetPlaylistTracks() len = %d, want 3", len(trackIDs))
	}
	if trackIDs[0] != "trk-1" || trackIDs[1] != "trk-2" || trackIDs[2] != "trk-3" {
		t.Fatalf("GetPlaylistTracks() = %#v, want [trk-1 trk-2 trk-3]", trackIDs)
	}
}

func TestGetPlaylistTracks_ValidationAndFailures(t *testing.T) {
	t.Parallel()

	t.Run("empty token", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "")
		_, err := adapter.GetPlaylistTracks(context.Background(), "pl")
		if err == nil || !strings.Contains(err.Error(), "oauth token") {
			t.Fatalf("expected oauth token error, got %v", err)
		}
	})

	t.Run("empty playlist id", func(t *testing.T) {
		adapter := NewAdapter(http.DefaultClient, "token")
		_, err := adapter.GetPlaylistTracks(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "playlist id is empty") {
			t.Fatalf("expected playlist id error, got %v", err)
		}
	})

	t.Run("non-200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer server.Close()

		adapter := NewAdapter(server.Client(), "token")
		adapter.baseURL = server.URL

		_, err := adapter.GetPlaylistTracks(context.Background(), "pl")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status %d", http.StatusForbidden)) {
			t.Fatalf("expected status error, got %v", err)
		}
	})
}

// TestGetPlaylistTracks_ExceedsMaxPages verifies that a server perpetually
// returning a non-empty "next" URL is stopped after maxPages iterations.
func TestGetPlaylistTracks_ExceedsMaxPages(t *testing.T) {
	t.Parallel()

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return a valid next URL pointing back at the same server so the
		// loop would run forever without the pagination guard.
		next := serverURL + "/v1/playlists/pl-loop/tracks"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"track": map[string]any{"id": "trk-x"}},
			},
			"next": next,
		})
	}))
	defer server.Close()
	serverURL = server.URL

	adapter := NewAdapter(server.Client(), "token-sp")
	adapter.baseURL = server.URL

	_, err := adapter.GetPlaylistTracks(context.Background(), "pl-loop")
	if err == nil || !strings.Contains(err.Error(), "exceeded max pagination pages") {
		t.Fatalf("expected pagination-limit error, got %v", err)
	}
}

// TestGetPlaylistTracks_NextURLHostMismatch verifies that a "next" URL whose
// host differs from the configured base URL is rejected (SSRF guard).
func TestGetPlaylistTracks_NextURLHostMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evil := "http://evil.example.com/whatever"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"track": map[string]any{"id": "trk-1"}},
			},
			"next": evil,
		})
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), "token-sp")
	adapter.baseURL = server.URL

	_, err := adapter.GetPlaylistTracks(context.Background(), "pl-123")
	if err == nil || !strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("expected host-mismatch error, got %v", err)
	}
}
