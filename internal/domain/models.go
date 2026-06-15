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
	ID               int
	YTPlaylistID     string
	YTPlaylistTitle  string
	SPPlaylistID     string
	SPPlaylistTitle  string
	Schedule         string     // plain-text schedule (e.g. "every day at 16", "once a week")
	NextScheduledRun *time.Time // next auto-sync time; nil = not scheduled
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
	YTVideoID           string
	YTTitle             string
	SPTrackID           string
	SPTitle             string
	SPArtist            string
	Confidence          float64
	Decision            MatchDecision
	DecisionSource      string // "matcher", "user", etc. — populated by GetMatch
	// Candidates holds the top Spotify search results for the current run.
	// Populated during dry-run; empty when loaded from track_matches.
	Candidates []TrackMatchCandidate
	// IsPriorChoice is true when the match was reused from a saved manual decision.
	IsPriorChoice bool
	// SyncedAt is set when the track was successfully added to the Spotify playlist.
	// Used to prevent automatic re-sync if the track is later removed from the playlist.
	SyncedAt *time.Time
	// ResyncRequestedAt is set when a user explicitly requests re-sync for this track.
	// If set and >= SyncedAt, the track will be re-matched in the next dry run.
	ResyncRequestedAt *time.Time
}

// TrackMatchCandidate is one Spotify search result stored per sync run for review.
type UserSettings struct {
	// TimeFormat controls timestamp display: "12h" or "24h".
	// Empty string defaults to the browser's locale preference (JS toLocaleString).
	TimeFormat string
}

type TrackMatchCandidate struct {
	SyncRunID  int
	YTVideoID  string
	SPTrackID  string
	SPTitle    string
	SPArtist   string
	Confidence float64
	Rank       int // 1-based position in the search results
}
