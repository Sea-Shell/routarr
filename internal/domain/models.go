package domain

import "time"

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
	YTVideoID  string
	YTTitle    string
	SPTrackID  string
	SPTitle    string
	SPArtist   string
	Confidence float64
	Decision   MatchDecision
}
