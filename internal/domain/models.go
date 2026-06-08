package domain

import "time"

// SyncRunEvent represents a single log entry emitted during a sync run.
// Level is one of "info", "success", "error".
type SyncRunEvent struct {
	ID        int
	RunID     int
	CreatedAt time.Time
	Level     string
	Message   string
	Details   string
}

type PlaylistMapping struct {
	ID              int
	YTPlaylistID    string
	YTPlaylistTitle string
	SPPlaylistID    string
	SPPlaylistTitle string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type SyncRun struct {
	ID         int
	MappingID  int
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string
}

type MatchDecision string

const (
	MatchAuto     MatchDecision = "auto"
	MatchApproved MatchDecision = "approved"
	MatchRejected MatchDecision = "rejected"
	MatchPending  MatchDecision = "pending"
)

type TrackMatch struct {
	YTVideoID      string
	YTTitle        string
	SPTrackID      string
	SPTitle        string
	SPArtist       string
	Confidence     float64
	Decision       MatchDecision
	DecisionSource string // "matcher", "user", etc. — populated by GetMatch
	// Candidates holds the top Spotify search results for the current run.
	// Populated during dry-run; empty when loaded from track_matches.
	Candidates []TrackMatchCandidate
	// IsPriorChoice is true when the match was reused from a saved manual decision.
	IsPriorChoice bool
}

// TrackMatchCandidate is one Spotify search result stored per sync run for review.
type TrackMatchCandidate struct {
	SyncRunID  int
	YTVideoID  string
	SPTrackID  string
	SPTitle    string
	SPArtist   string
	Confidence float64
	Rank       int // 1-based position in the search results
}
