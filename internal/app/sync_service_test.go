package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bateau84/yt2sp/internal/domain"
)

func TestRunDrySkipsPreviouslyRejected(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 10, YTPlaylistID: "yt-playlist"}
	rejected := &domain.TrackMatch{YTVideoID: "video-1", Decision: domain.MatchRejected}

	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{existing: map[string]*domain.TrackMatch{"video-1": rejected}}
	yt := &youtubeServiceStub{videos: []domain.TrackMatch{{YTVideoID: "video-1", YTTitle: "Artist - Track"}}}
	sp := &spotifyServiceStub{}
	matcher := &matcherStub{}

	svc := NewSyncService(yt, sp, mappingRepo, matchRepo, matcher)

	run, matches, err := svc.RunDry(context.Background(), mapping.ID)
	if err != nil {
		t.Fatalf("RunDry() error = %v", err)
	}
	if run == nil {
		t.Fatalf("RunDry() run is nil")
	}
	if run.Status != syncRunStatusPending {
		t.Fatalf("run status = %q, want %q", run.Status, syncRunStatusPending)
	}
	if len(matches) != 0 {
		t.Fatalf("RunDry() returned %d matches, want 0", len(matches))
	}

	if sp.searchCalls != 0 {
		t.Fatalf("SearchTrack called %d times, want 0", sp.searchCalls)
	}
	if matcher.normalizeCalls != 0 {
		t.Fatalf("Normalize called %d times, want 0", matcher.normalizeCalls)
	}
	if matchRepo.saveCalls != 0 {
		t.Fatalf("SaveMatch called %d times, want 0", matchRepo.saveCalls)
	}
	if mappingRepo.saveRunCalls != 1 {
		t.Fatalf("SaveSyncRun called %d times, want 1", mappingRepo.saveRunCalls)
	}
}

func TestRunDryMarksHighConfidenceAsAuto(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 11, YTPlaylistID: "yt-playlist"}
	videos := []domain.TrackMatch{{YTVideoID: "video-2", YTTitle: "Nirvana - Smells Like Teen Spirit"}}
	candidate := &domain.TrackMatch{SPTrackID: "sp-1", SPTitle: "Smells Like Teen Spirit", SPArtist: "Nirvana"}

	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{existing: map[string]*domain.TrackMatch{}}
	yt := &youtubeServiceStub{videos: videos}
	sp := &spotifyServiceStub{candidate: candidate}
	matcher := &matcherStub{
		normalized: "nirvana smells like teen spirit",
		score:      0.91,
		decision:   domain.MatchAuto,
	}

	svc := NewSyncService(yt, sp, mappingRepo, matchRepo, matcher)

	run, matches, err := svc.RunDry(context.Background(), mapping.ID)
	if err != nil {
		t.Fatalf("RunDry() error = %v", err)
	}
	if run == nil {
		t.Fatalf("RunDry() run is nil")
	}
	if len(matches) != 1 {
		t.Fatalf("RunDry() returned %d matches, want 1", len(matches))
	}

	got := matches[0]
	if got.Decision != domain.MatchAuto {
		t.Fatalf("decision = %q, want %q", got.Decision, domain.MatchAuto)
	}
	if got.Confidence != matcher.score {
		t.Fatalf("confidence = %v, want %v", got.Confidence, matcher.score)
	}
	if got.SPTrackID != candidate.SPTrackID {
		t.Fatalf("sp track id = %q, want %q", got.SPTrackID, candidate.SPTrackID)
	}

	if sp.searchCalls != 1 {
		t.Fatalf("SearchTrack called %d times, want 1", sp.searchCalls)
	}
	if matcher.normalizeCalls != 1 {
		t.Fatalf("Normalize called %d times, want 1", matcher.normalizeCalls)
	}
	if matcher.scoreCalls != 1 {
		t.Fatalf("Score called %d times, want 1", matcher.scoreCalls)
	}
	if matcher.classifyCalls != 1 {
		t.Fatalf("Classify called %d times, want 1", matcher.classifyCalls)
	}
	if matchRepo.saveCalls != 1 {
		t.Fatalf("SaveMatch called %d times, want 1", matchRepo.saveCalls)
	}
	if mappingRepo.saveRunCalls != 1 {
		t.Fatalf("SaveSyncRun called %d times, want 1", mappingRepo.saveRunCalls)
	}
}

func TestRunDryReturnsContextErrorWhenCanceledMidLoop(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 12, YTPlaylistID: "yt-playlist"}
	videos := []domain.TrackMatch{
		{YTVideoID: "video-1", YTTitle: "Artist One - Track One"},
		{YTVideoID: "video-2", YTTitle: "Artist Two - Track Two"},
	}

	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{existing: map[string]*domain.TrackMatch{}}
	ctx, cancel := context.WithCancel(context.Background())

	sp := &spotifyServiceStub{
		candidate: &domain.TrackMatch{SPTrackID: "sp-1", SPTitle: "Track One", SPArtist: "Artist One"},
		onSearch: func() {
			cancel()
		},
	}

	svc := NewSyncService(
		&youtubeServiceStub{videos: videos},
		sp,
		mappingRepo,
		matchRepo,
		&matcherStub{normalized: "artist track", score: 0.85, decision: domain.MatchAuto},
	)

	run, matches, err := svc.RunDry(ctx, mapping.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunDry() error = %v, want context.Canceled", err)
	}
	if run != nil || matches != nil {
		t.Fatalf("RunDry() expected nil run and matches on cancel, got run=%v matches=%v", run, matches)
	}
	if sp.searchCalls != 1 {
		t.Fatalf("SearchTrack called %d times, want 1", sp.searchCalls)
	}
}

type mappingRepoStub struct {
	mapping      *domain.PlaylistMapping
	err          error
	saveRunCalls int
	savedRuns    []*domain.SyncRun
}

func (s *mappingRepoStub) Save(ctx context.Context, m *domain.PlaylistMapping) error {
	return nil
}

func (s *mappingRepoStub) GetByID(ctx context.Context, id int) (*domain.PlaylistMapping, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.mapping != nil && s.mapping.ID == id {
		return s.mapping, nil
	}
	return nil, nil
}

func (s *mappingRepoStub) List(ctx context.Context) ([]domain.PlaylistMapping, error) {
	return nil, nil
}

func (s *mappingRepoStub) SaveSyncRun(ctx context.Context, run *domain.SyncRun) error {
	s.saveRunCalls++
	if run.ID == 0 {
		run.ID = s.saveRunCalls
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	s.savedRuns = append(s.savedRuns, run)
	return nil
}

type matchRepoStub struct {
	existing   map[string]*domain.TrackMatch
	saveCalls  int
	savedMatch []*domain.TrackMatch
}

func (s *matchRepoStub) SaveMatch(ctx context.Context, m *domain.TrackMatch) error {
	s.saveCalls++
	if s.existing == nil {
		s.existing = make(map[string]*domain.TrackMatch)
	}
	copyMatch := *m
	s.existing[m.YTVideoID] = &copyMatch
	s.savedMatch = append(s.savedMatch, &copyMatch)
	return nil
}

func (s *matchRepoStub) GetMatch(ctx context.Context, ytVideoID string) (*domain.TrackMatch, error) {
	if s.existing == nil {
		return nil, nil
	}
	if m, ok := s.existing[ytVideoID]; ok {
		copyMatch := *m
		return &copyMatch, nil
	}
	return nil, nil
}

type youtubeServiceStub struct {
	videos []domain.TrackMatch
	err    error
}

func (s *youtubeServiceStub) GetPlaylistVideos(ctx context.Context, playlistID string) ([]domain.TrackMatch, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.videos, nil
}

type spotifyServiceStub struct {
	candidate   *domain.TrackMatch
	err         error
	searchCalls int
	onSearch    func()
}

func (s *spotifyServiceStub) SearchTrack(ctx context.Context, query string) (*domain.TrackMatch, error) {
	s.searchCalls++
	if s.onSearch != nil {
		s.onSearch()
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.candidate == nil {
		return nil, nil
	}
	copyCandidate := *s.candidate
	return &copyCandidate, nil
}

func (s *spotifyServiceStub) AddTrackToPlaylist(ctx context.Context, playlistID, trackID string) error {
	return nil
}

type matcherStub struct {
	normalized    string
	score         float64
	decision      domain.MatchDecision
	normalizeCalls int
	scoreCalls    int
	classifyCalls int
}

func (s *matcherStub) Normalize(title string) string {
	s.normalizeCalls++
	if s.normalized != "" {
		return s.normalized
	}
	return title
}

func (s *matcherStub) Score(videoTitle, candidateTitle, artist string) float64 {
	s.scoreCalls++
	return s.score
}

func (s *matcherStub) Classify(score float64) domain.MatchDecision {
	s.classifyCalls++
	if s.decision != "" {
		return s.decision
	}
	return domain.MatchPending
}
