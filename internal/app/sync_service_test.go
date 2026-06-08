package app

import (
	"context"
	"errors"
	"fmt"
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

func TestCommitAddsEligibleTracksSkipsDuplicatesAndCompletesRun(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 21, SPPlaylistID: "sp-playlist-1"}
	run := &domain.SyncRun{ID: 7, MappingID: mapping.ID, Status: syncRunStatusPending}

	mappingRepo := &mappingRepoStub{
		mapping: mapping,
		runs:    map[int]*domain.SyncRun{run.ID: run},
		runItems: map[int][]domain.TrackMatch{
			run.ID: {
				{YTVideoID: "yt-auto", SPTrackID: "sp-auto", Decision: domain.MatchAuto},
				{YTVideoID: "yt-approved", SPTrackID: "sp-approved", Decision: domain.MatchApproved},
				{YTVideoID: "yt-duplicate", SPTrackID: "sp-existing", Decision: domain.MatchApproved},
				{YTVideoID: "yt-pending", SPTrackID: "sp-pending", Decision: domain.MatchPending},
				{YTVideoID: "yt-rejected", SPTrackID: "sp-rejected", Decision: domain.MatchRejected},
			},
		},
	}

	sp := &spotifyServiceStub{playlistTracks: []string{"sp-existing"}}

	svc := NewSyncService(
		&youtubeServiceStub{},
		sp,
		mappingRepo,
		&matchRepoStub{},
		&matcherStub{},
	)

	err := svc.Commit(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if sp.getPlaylistTracksCalls != 1 {
		t.Fatalf("GetPlaylistTracks called %d times, want 1", sp.getPlaylistTracksCalls)
	}
	if len(sp.addedTracks) != 2 {
		t.Fatalf("AddTrackToPlaylist called %d times, want 2", len(sp.addedTracks))
	}
	if sp.addedTracks[0].trackID != "sp-auto" || sp.addedTracks[1].trackID != "sp-approved" {
		t.Fatalf("added tracks = %#v, want sp-auto and sp-approved", sp.addedTracks)
	}

	if len(mappingRepo.actionUpdates) != 3 {
		t.Fatalf("sync item updates = %d, want 3", len(mappingRepo.actionUpdates))
	}

	actionsByVideo := make(map[string]string, len(mappingRepo.actionUpdates))
	for _, update := range mappingRepo.actionUpdates {
		actionsByVideo[update.ytVideoID] = update.action
	}
	if actionsByVideo["yt-auto"] != syncItemActionAdded {
		t.Fatalf("yt-auto action = %q, want %q", actionsByVideo["yt-auto"], syncItemActionAdded)
	}
	if actionsByVideo["yt-approved"] != syncItemActionAdded {
		t.Fatalf("yt-approved action = %q, want %q", actionsByVideo["yt-approved"], syncItemActionAdded)
	}
	if actionsByVideo["yt-duplicate"] != syncItemActionSkippedDuplicate {
		t.Fatalf("yt-duplicate action = %q, want %q", actionsByVideo["yt-duplicate"], syncItemActionSkippedDuplicate)
	}
	if _, ok := actionsByVideo["yt-pending"]; ok {
		t.Fatalf("yt-pending should not be updated, got %q", actionsByVideo["yt-pending"])
	}
	if _, ok := actionsByVideo["yt-rejected"]; ok {
		t.Fatalf("yt-rejected should not be updated, got %q", actionsByVideo["yt-rejected"])
	}

	if len(mappingRepo.statusUpdates) != 1 {
		t.Fatalf("sync run status updates = %d, want 1", len(mappingRepo.statusUpdates))
	}
	if mappingRepo.statusUpdates[0].status != syncRunStatusCompleted {
		t.Fatalf("run status = %q, want %q", mappingRepo.statusUpdates[0].status, syncRunStatusCompleted)
	}
}

type mappingRepoStub struct {
	mapping      *domain.PlaylistMapping
	err          error
	saveRunCalls int
	savedRuns    []*domain.SyncRun
	runs         map[int]*domain.SyncRun
	runItems     map[int][]domain.TrackMatch
	actionUpdates []syncItemActionUpdate
	statusUpdates []syncRunStatusUpdate
}

type syncItemActionUpdate struct {
	runID     int
	ytVideoID string
	action    string
	itemError string
}

type syncRunStatusUpdate struct {
	runID      int
	status     string
	finishedAt time.Time
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
	if s.runs == nil {
		s.runs = make(map[int]*domain.SyncRun)
	}
	s.runs[run.ID] = run
	return nil
}

func (s *mappingRepoStub) GetSyncRunByID(ctx context.Context, runID int) (*domain.SyncRun, error) {
	if s.runs == nil {
		return nil, nil
	}
	run, ok := s.runs[runID]
	if !ok {
		return nil, nil
	}
	copyRun := *run
	return &copyRun, nil
}

func (s *mappingRepoStub) ListSyncRunMatches(ctx context.Context, runID int) ([]domain.TrackMatch, error) {
	if s.runItems == nil {
		return nil, nil
	}
	items := s.runItems[runID]
	cloned := make([]domain.TrackMatch, len(items))
	copy(cloned, items)
	return cloned, nil
}

func (s *mappingRepoStub) UpdateSyncItemAction(ctx context.Context, runID int, ytVideoID, action, itemError string) error {
	s.actionUpdates = append(s.actionUpdates, syncItemActionUpdate{
		runID:     runID,
		ytVideoID: ytVideoID,
		action:    action,
		itemError: itemError,
	})
	return nil
}

func (s *mappingRepoStub) UpdateSyncRunStatus(ctx context.Context, runID int, status string, finishedAt time.Time) error {
	s.statusUpdates = append(s.statusUpdates, syncRunStatusUpdate{
		runID:      runID,
		status:     status,
		finishedAt: finishedAt,
	})
	if s.runs != nil {
		if run, ok := s.runs[runID]; ok {
			run.Status = status
			run.FinishedAt = &finishedAt
		}
	}
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

func (s *matchRepoStub) UpdateMatchChoice(ctx context.Context, ytVideoID, spTrackID, spTitle, spArtist string, decision domain.MatchDecision) error {
	if s.existing == nil {
		s.existing = make(map[string]*domain.TrackMatch)
	}
	if m, ok := s.existing[ytVideoID]; ok {
		m.SPTrackID = spTrackID
		m.SPTitle = spTitle
		m.SPArtist = spArtist
		m.Decision = decision
		m.DecisionSource = "user"
	}
	return nil
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
	candidate              *domain.TrackMatch
	err                    error
	searchCalls            int
	onSearch               func()
	onAdd                  func(playlistID, trackID string)
	playlistTracks         []string
	getPlaylistTracksCalls int
	addedTracks            []addedTrackCall
	addErrByTrackID        map[string]error
}

type addedTrackCall struct {
	playlistID string
	trackID    string
}

// SearchTrack is kept for interface compliance; sync service now uses SearchTracks.
func (s *spotifyServiceStub) SearchTrack(ctx context.Context, query string) (*domain.TrackMatch, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.candidate == nil {
		return nil, nil
	}
	copyCandidate := *s.candidate
	return &copyCandidate, nil
}

// SearchTracks satisfies the updated SpotifyService interface.
// It converts the single stub candidate into a []TrackMatchCandidate slice.
func (s *spotifyServiceStub) SearchTracks(ctx context.Context, query string, limit int) ([]domain.TrackMatchCandidate, error) {
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
	return []domain.TrackMatchCandidate{
		{
			SPTrackID: s.candidate.SPTrackID,
			SPTitle:   s.candidate.SPTitle,
			SPArtist:  s.candidate.SPArtist,
			Rank:      1,
		},
	}, nil
}

func (s *spotifyServiceStub) AddTrackToPlaylist(ctx context.Context, playlistID, trackID string) error {
	s.addedTracks = append(s.addedTracks, addedTrackCall{playlistID: playlistID, trackID: trackID})
	if s.onAdd != nil {
		s.onAdd(playlistID, trackID)
	}
	if s.addErrByTrackID != nil {
		if err, ok := s.addErrByTrackID[trackID]; ok {
			return err
		}
	}
	return nil
}

func (s *spotifyServiceStub) GetPlaylistTracks(ctx context.Context, playlistID string) ([]string, error) {
	s.getPlaylistTracksCalls++
	if s.err != nil {
		return nil, fmt.Errorf("get playlist tracks: %w", s.err)
	}
	tracks := make([]string, len(s.playlistTracks))
	copy(tracks, s.playlistTracks)
	return tracks, nil
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

// TestCommitContinuesAfterAddTrackFailure verifies that a single failed AddTrackToPlaylist
// does not abort the loop: the failing item is marked failed, the remaining item is still
// added, and the run finishes with status=completed.
func TestCommitContinuesAfterAddTrackFailure(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 30, SPPlaylistID: "sp-playlist-2"}
	run := &domain.SyncRun{ID: 10, MappingID: mapping.ID, Status: syncRunStatusPending}

	mappingRepo := &mappingRepoStub{
		mapping: mapping,
		runs:    map[int]*domain.SyncRun{run.ID: run},
		runItems: map[int][]domain.TrackMatch{
			run.ID: {
				{YTVideoID: "yt-fail", SPTrackID: "sp-fail", Decision: domain.MatchApproved},
				{YTVideoID: "yt-ok", SPTrackID: "sp-ok", Decision: domain.MatchApproved},
			},
		},
	}

	sp := &spotifyServiceStub{
		addErrByTrackID: map[string]error{
			"sp-fail": fmt.Errorf("spotify API error"),
		},
	}

	svc := NewSyncService(&youtubeServiceStub{}, sp, mappingRepo, &matchRepoStub{}, &matcherStub{})

	if err := svc.Commit(context.Background(), run.ID); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// Both tracks attempted.
	if len(sp.addedTracks) != 2 {
		t.Fatalf("AddTrackToPlaylist called %d times, want 2", len(sp.addedTracks))
	}

	actionsByVideo := make(map[string]string, len(mappingRepo.actionUpdates))
	for _, u := range mappingRepo.actionUpdates {
		actionsByVideo[u.ytVideoID] = u.action
	}
	if actionsByVideo["yt-fail"] != syncItemActionFailed {
		t.Fatalf("yt-fail action = %q, want %q", actionsByVideo["yt-fail"], syncItemActionFailed)
	}
	if actionsByVideo["yt-ok"] != syncItemActionAdded {
		t.Fatalf("yt-ok action = %q, want %q", actionsByVideo["yt-ok"], syncItemActionAdded)
	}

	if len(mappingRepo.statusUpdates) != 1 {
		t.Fatalf("status updates = %d, want 1", len(mappingRepo.statusUpdates))
	}
	if mappingRepo.statusUpdates[0].status != syncRunStatusCompleted {
		t.Fatalf("run status = %q, want %q", mappingRepo.statusUpdates[0].status, syncRunStatusCompleted)
	}
}

// TestCommitReturnsErrSyncRunNotFoundWhenRunMissing verifies that Commit returns
// ErrSyncRunNotFound (unwrapped via errors.Is) when the run ID does not exist.
func TestCommitReturnsErrSyncRunNotFoundWhenRunMissing(t *testing.T) {
	t.Parallel()

	mappingRepo := &mappingRepoStub{
		mapping: &domain.PlaylistMapping{ID: 1, SPPlaylistID: "sp-playlist"},
		runs:    map[int]*domain.SyncRun{}, // deliberately empty
	}

	svc := NewSyncService(&youtubeServiceStub{}, &spotifyServiceStub{}, mappingRepo, &matchRepoStub{}, &matcherStub{})

	err := svc.Commit(context.Background(), 99)
	if err == nil {
		t.Fatal("Commit() expected error, got nil")
	}
	if !errors.Is(err, ErrSyncRunNotFound) {
		t.Fatalf("Commit() error = %v, want ErrSyncRunNotFound", err)
	}
}

// TestCommitReturnsErrorWhenGetPlaylistTracksFails verifies that a GetPlaylistTracks
// failure causes Commit to return early: no item actions are recorded and the run
// status is never updated.
func TestCommitReturnsErrorWhenGetPlaylistTracksFails(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 31, SPPlaylistID: "sp-playlist-3"}
	run := &domain.SyncRun{ID: 11, MappingID: mapping.ID, Status: syncRunStatusPending}

	mappingRepo := &mappingRepoStub{
		mapping: mapping,
		runs:    map[int]*domain.SyncRun{run.ID: run},
		runItems: map[int][]domain.TrackMatch{
			run.ID: {
				{YTVideoID: "yt-1", SPTrackID: "sp-1", Decision: domain.MatchApproved},
			},
		},
	}

	sentinelErr := errors.New("spotify unavailable")
	sp := &spotifyServiceStub{err: sentinelErr}

	svc := NewSyncService(&youtubeServiceStub{}, sp, mappingRepo, &matchRepoStub{}, &matcherStub{})

	if err := svc.Commit(context.Background(), run.ID); err == nil {
		t.Fatal("Commit() expected error, got nil")
	}
	if len(mappingRepo.actionUpdates) != 0 {
		t.Fatalf("actionUpdates = %d, want 0", len(mappingRepo.actionUpdates))
	}
	if len(mappingRepo.statusUpdates) != 0 {
		t.Fatalf("statusUpdates = %d, want 0", len(mappingRepo.statusUpdates))
	}
}

// TestCommitContextCancelledMidLoop verifies that a context cancellation that occurs
// inside the item loop causes Commit to stop processing and return context.Canceled.
func TestCommitContextCancelledMidLoop(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 32, SPPlaylistID: "sp-playlist-4"}
	run := &domain.SyncRun{ID: 12, MappingID: mapping.ID, Status: syncRunStatusPending}

	mappingRepo := &mappingRepoStub{
		mapping: mapping,
		runs:    map[int]*domain.SyncRun{run.ID: run},
		runItems: map[int][]domain.TrackMatch{
			run.ID: {
				{YTVideoID: "yt-first", SPTrackID: "sp-first", Decision: domain.MatchApproved},
				{YTVideoID: "yt-second", SPTrackID: "sp-second", Decision: domain.MatchApproved},
				{YTVideoID: "yt-third", SPTrackID: "sp-third", Decision: domain.MatchApproved},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	sp := &spotifyServiceStub{
		// Cancel the context after the very first AddTrackToPlaylist succeeds.
		onAdd: func(_, _ string) { cancel() },
	}

	svc := NewSyncService(&youtubeServiceStub{}, sp, mappingRepo, &matchRepoStub{}, &matcherStub{})

	err := svc.Commit(ctx, run.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit() error = %v, want context.Canceled", err)
	}
	// Only the first item was processed; the loop stopped before reaching the others.
	if len(sp.addedTracks) >= 3 {
		t.Fatalf("addedTracks = %d, want < 3 (loop should have stopped early)", len(sp.addedTracks))
	}
}

// TestCommitSkipsNonEligibleItemsAndCompletes verifies that when all items have
// MatchPending or MatchRejected decisions, GetPlaylistTracks is still called once
// for dedup initialisation, AddTrackToPlaylist is never called, and the run
// finishes with status=completed.
func TestCommitSkipsNonEligibleItemsAndCompletes(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 33, SPPlaylistID: "sp-playlist-5"}
	run := &domain.SyncRun{ID: 13, MappingID: mapping.ID, Status: syncRunStatusPending}

	mappingRepo := &mappingRepoStub{
		mapping: mapping,
		runs:    map[int]*domain.SyncRun{run.ID: run},
		runItems: map[int][]domain.TrackMatch{
			run.ID: {
				{YTVideoID: "yt-pending", SPTrackID: "sp-pending", Decision: domain.MatchPending},
				{YTVideoID: "yt-rejected", SPTrackID: "sp-rejected", Decision: domain.MatchRejected},
			},
		},
	}

	sp := &spotifyServiceStub{}

	svc := NewSyncService(&youtubeServiceStub{}, sp, mappingRepo, &matchRepoStub{}, &matcherStub{})

	if err := svc.Commit(context.Background(), run.ID); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if sp.getPlaylistTracksCalls != 1 {
		t.Fatalf("GetPlaylistTracks called %d times, want 1", sp.getPlaylistTracksCalls)
	}
	if len(sp.addedTracks) != 0 {
		t.Fatalf("AddTrackToPlaylist called %d times, want 0", len(sp.addedTracks))
	}
	if len(mappingRepo.statusUpdates) != 1 {
		t.Fatalf("status updates = %d, want 1", len(mappingRepo.statusUpdates))
	}
	if mappingRepo.statusUpdates[0].status != syncRunStatusCompleted {
		t.Fatalf("run status = %q, want %q", mappingRepo.statusUpdates[0].status, syncRunStatusCompleted)
	}
}

// TestRunDryReusesPriorManualChoiceAndSetsIsPriorChoice verifies that when a manual
// decision already exists for a video, RunDry reuses it and marks IsPriorChoice=true.
func TestRunDryReusesPriorManualChoiceAndSetsIsPriorChoice(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 40, YTPlaylistID: "yt-playlist"}
	approved := &domain.TrackMatch{
		YTVideoID:      "video-prior",
		YTTitle:        "Artist - Track",
		SPTrackID:      "sp-prior",
		SPTitle:        "Track",
		SPArtist:       "Artist",
		Decision:       domain.MatchApproved,
		DecisionSource: "user", // must be "user" for IsPriorChoice to be set
	}

	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{existing: map[string]*domain.TrackMatch{"video-prior": approved}}
	yt := &youtubeServiceStub{videos: []domain.TrackMatch{{YTVideoID: "video-prior", YTTitle: "Artist - Track"}}}
	sp := &spotifyServiceStub{}

	svc := NewSyncService(yt, sp, mappingRepo, matchRepo, &matcherStub{})

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
	if got.SPTrackID != "sp-prior" {
		t.Fatalf("SPTrackID = %q, want %q", got.SPTrackID, "sp-prior")
	}
	if !got.IsPriorChoice {
		t.Fatal("IsPriorChoice = false, want true")
	}
	// SearchTracks must not be called when reusing a prior choice.
	if sp.searchCalls != 0 {
		t.Fatalf("SearchTracks called %d times, want 0", sp.searchCalls)
	}
}

// TestRunDrySavesTopCandidatesAndPicksBest verifies that with a candidateRepo wired,
// RunDry stores all scored candidates and chooses the highest-confidence one.
func TestRunDrySavesTopCandidatesAndPicksBest(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 41, YTPlaylistID: "yt-playlist"}
	video := domain.TrackMatch{YTVideoID: "video-new", YTTitle: "Nirvana - Come As You Are"}

	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{existing: map[string]*domain.TrackMatch{}}
	yt := &youtubeServiceStub{videos: []domain.TrackMatch{video}}
	candRepo := &candidateRepoStub{}

	sp := &spotifyServiceStubMulti{
		candidates: []domain.TrackMatchCandidate{
			{SPTrackID: "sp-best", SPTitle: "Come As You Are", SPArtist: "Nirvana", Rank: 1},
			{SPTrackID: "sp-mid", SPTitle: "Heart-Shaped Box", SPArtist: "Nirvana", Rank: 2},
			{SPTrackID: "sp-low", SPTitle: "Something Else", SPArtist: "Other", Rank: 3},
		},
	}

	// scorer always returns 0.9 for rank-1 title, lower for others
	matcher := &rankAwareMatcherStub{
		scores: map[string]float64{
			"Come As You Are": 0.92,
			"Heart-Shaped Box": 0.40,
			"Something Else":  0.15,
		},
		defaultDecision: domain.MatchAuto,
	}

	svc := NewSyncService(yt, sp, mappingRepo, matchRepo, matcher, WithCandidateRepository(candRepo))

	_, matches, err := svc.RunDry(context.Background(), mapping.ID)
	if err != nil {
		t.Fatalf("RunDry() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("RunDry() returned %d matches, want 1", len(matches))
	}

	got := matches[0]
	if got.SPTrackID != "sp-best" {
		t.Fatalf("best candidate SPTrackID = %q, want %q", got.SPTrackID, "sp-best")
	}
	if got.Confidence != 0.92 {
		t.Fatalf("best candidate confidence = %v, want 0.92", got.Confidence)
	}
	if got.IsPriorChoice {
		t.Fatal("IsPriorChoice = true, want false for fresh search")
	}

	// CandidateRepo must have received 3 candidates.
	if len(candRepo.saved) != 3 {
		t.Fatalf("saved candidates = %d, want 3", len(candRepo.saved))
	}
}

// ── Additional stubs for new tests ──────────────────────────────────────────

type candidateRepoStub struct {
	saved  []domain.TrackMatchCandidate
	saveErr error
}

func (s *candidateRepoStub) SaveCandidates(_ context.Context, candidates []domain.TrackMatchCandidate) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = append(s.saved, candidates...)
	return nil
}

func (s *candidateRepoStub) GetCandidates(_ context.Context, syncRunID int, ytVideoID string) ([]domain.TrackMatchCandidate, error) {
	var out []domain.TrackMatchCandidate
	for _, c := range s.saved {
		if c.SyncRunID == syncRunID && c.YTVideoID == ytVideoID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *candidateRepoStub) GetCandidatesByRun(_ context.Context, syncRunID int) (map[string][]domain.TrackMatchCandidate, error) {
	result := make(map[string][]domain.TrackMatchCandidate)
	for _, c := range s.saved {
		if c.SyncRunID == syncRunID {
			result[c.YTVideoID] = append(result[c.YTVideoID], c)
		}
	}
	return result, nil
}

// spotifyServiceStubMulti returns a configurable slice of candidates.
type spotifyServiceStubMulti struct {
	candidates []domain.TrackMatchCandidate
	err        error
}

func (s *spotifyServiceStubMulti) SearchTrack(_ context.Context, _ string) (*domain.TrackMatch, error) {
	return nil, nil
}

func (s *spotifyServiceStubMulti) SearchTracks(_ context.Context, _ string, _ int) ([]domain.TrackMatchCandidate, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]domain.TrackMatchCandidate, len(s.candidates))
	copy(out, s.candidates)
	return out, nil
}

func (s *spotifyServiceStubMulti) GetPlaylistTracks(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (s *spotifyServiceStubMulti) AddTrackToPlaylist(_ context.Context, _, _ string) error {
	return nil
}

// rankAwareMatcherStub scores by candidate title from a map.
type rankAwareMatcherStub struct {
	scores          map[string]float64
	defaultDecision domain.MatchDecision
}

func (m *rankAwareMatcherStub) Normalize(title string) string { return title }

func (m *rankAwareMatcherStub) Score(_, candidateTitle, _ string) float64 {
	if s, ok := m.scores[candidateTitle]; ok {
		return s
	}
	return 0
}

func (m *rankAwareMatcherStub) Classify(_ float64) domain.MatchDecision {
	if m.defaultDecision != "" {
		return m.defaultDecision
	}
	return domain.MatchPending
}

// fakeProgressReporter is a test double for ports.ProgressReporter.
type fakeProgressReporter struct {
	calls []progressReportCall
}

type progressReportCall struct {
	runID   int
	level   string
	message string
}

func (f *fakeProgressReporter) Report(_ context.Context, runID int, level, message string) {
	f.calls = append(f.calls, progressReportCall{runID: runID, level: level, message: message})
}

// TestRunDryReportsProgressEvents verifies that RunDry emits the expected
// ordered progress events when a reporter is wired and the run succeeds.
func TestRunDryReportsProgressEvents(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 50, YTPlaylistID: "yt-playlist"}
	videos := []domain.TrackMatch{
		{YTVideoID: "vid-a", YTTitle: "Artist A - Track A"},
		{YTVideoID: "vid-b", YTTitle: "Artist B - Track B"},
	}
	candidate := &domain.TrackMatch{SPTrackID: "sp-1", SPTitle: "Track A", SPArtist: "Artist A"}

	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{existing: map[string]*domain.TrackMatch{}}
	yt := &youtubeServiceStub{videos: videos}
	sp := &spotifyServiceStub{candidate: candidate}
	matcher := &matcherStub{score: 0.91, decision: domain.MatchAuto}
	reporter := &fakeProgressReporter{}

	svc := NewSyncService(yt, sp, mappingRepo, matchRepo, matcher,
		WithProgressReporter(reporter),
	)

	run, _, err := svc.RunDry(context.Background(), mapping.ID)
	if err != nil {
		t.Fatalf("RunDry() error = %v", err)
	}
	if run == nil {
		t.Fatalf("RunDry() run is nil")
	}

	// All events must carry the saved run ID.
	for i, call := range reporter.calls {
		if call.runID != run.ID {
			t.Errorf("event[%d] runID = %d, want %d", i, call.runID, run.ID)
		}
	}

	// Verify minimum required events in order.
	type wantEvent struct {
		level   string
		message string
	}
	want := []wantEvent{
		{"info", "Created dry sync run"},
		{"info", "Fetching YouTube playlist"},
		{"info", "Fetched 2 YouTube videos"},
		{"info", "Matching video 1/2: Artist A - Track A"},
		{"info", "Matching video 2/2: Artist B - Track B"},
		{"success", "Finished matching"},
	}

	if len(reporter.calls) < len(want) {
		t.Fatalf("got %d progress events, want at least %d; events: %+v", len(reporter.calls), len(want), reporter.calls)
	}

	// Build an ordered sequence for exact-order check.
	got := make([]wantEvent, len(reporter.calls))
	for i, c := range reporter.calls {
		got[i] = wantEvent{c.level, c.message}
	}

	// Check that want events appear as a subsequence (in order) within got.
	wi := 0
	for _, g := range got {
		if wi < len(want) && g == want[wi] {
			wi++
		}
	}
	if wi != len(want) {
		t.Errorf("expected progress events not found as ordered subsequence.\nwant: %+v\ngot:  %+v", want, got)
	}
}

// TestRunDryReportsFetchError verifies that when YouTube playlist fetch fails
// after the run is created, an error-level event is emitted with the run ID.
func TestRunDryReportsFetchError(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 51, YTPlaylistID: "yt-error-playlist"}
	fetchErr := errors.New("network timeout")

	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{}
	yt := &youtubeServiceStub{err: fetchErr}
	sp := &spotifyServiceStub{}
	matcher := &matcherStub{}
	reporter := &fakeProgressReporter{}

	svc := NewSyncService(yt, sp, mappingRepo, matchRepo, matcher,
		WithProgressReporter(reporter),
	)

	run, _, err := svc.RunDry(context.Background(), mapping.ID)
	if err == nil {
		t.Fatal("RunDry() expected error, got nil")
	}
	// run may be returned even on error (run was created before fetch)
	_ = run

	// Must have at least one error-level event.
	var errEvents []progressReportCall
	for _, c := range reporter.calls {
		if c.level == "error" {
			errEvents = append(errEvents, c)
		}
	}
	if len(errEvents) == 0 {
		t.Fatalf("expected at least one error-level progress event, got none; all events: %+v", reporter.calls)
	}

	// The error event must have a valid run ID (> 0 since run was created).
	for _, e := range errEvents {
		if e.runID <= 0 {
			t.Errorf("error event runID = %d, want > 0", e.runID)
		}
	}
}

// TestWithProgressReporterWiresReporter verifies that WithProgressReporter
// stores the reporter on the SyncService so it is available for use later.
func TestWithProgressReporterWiresReporter(t *testing.T) {
	t.Parallel()

	mapping := &domain.PlaylistMapping{ID: 1, YTPlaylistID: "yt-playlist"}
	mappingRepo := &mappingRepoStub{mapping: mapping}
	matchRepo := &matchRepoStub{}
	yt := &youtubeServiceStub{}
	sp := &spotifyServiceStub{}
	matcher := &matcherStub{}

	reporter := &fakeProgressReporter{}

	svc := NewSyncService(yt, sp, mappingRepo, matchRepo, matcher,
		WithProgressReporter(reporter),
	)

	if svc.progressReporter == nil {
		t.Fatal("expected progressReporter to be set, got nil")
	}
	if svc.progressReporter != reporter {
		t.Fatal("expected progressReporter to be the supplied reporter instance")
	}
}
