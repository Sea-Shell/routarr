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
