package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bateau84/yt2sp/internal/domain"
)

func TestOpenAndRunMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "yt2sp.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("close db: %v", closeErr)
		}
	})

	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("RunMigrations() should be idempotent, got error: %v", err)
	}

	var migrationCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}

	if migrationCount != len(migrations) {
		t.Fatalf("schema migrations count = %d, want %d", migrationCount, len(migrations))
	}
}

func TestMappingRepositoryCRUD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "yt2sp.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("close db: %v", closeErr)
		}
	})

	repo := NewMappingRepository(db)

	mapping := &domain.PlaylistMapping{
		YTPlaylistID:    "yt-playlist-1",
		YTPlaylistTitle: "YouTube Favorites",
		SPPlaylistID:    "sp-playlist-1",
		SPPlaylistTitle: "Spotify Favorites",
	}

	if err := repo.Save(ctx, mapping); err != nil {
		t.Fatalf("Save(create) error = %v", err)
	}

	if mapping.ID == 0 {
		t.Fatalf("Save(create) did not set ID")
	}
	if mapping.CreatedAt.IsZero() {
		t.Fatalf("Save(create) did not set CreatedAt")
	}
	if mapping.UpdatedAt.IsZero() {
		t.Fatalf("Save(create) did not set UpdatedAt")
	}

	stored, err := repo.GetByID(ctx, mapping.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored == nil {
		t.Fatalf("GetByID() returned nil mapping")
	}
	if stored.YTPlaylistID != mapping.YTPlaylistID || stored.SPPlaylistID != mapping.SPPlaylistID {
		t.Fatalf("GetByID() returned unexpected mapping: %+v", stored)
	}

	mapping.YTPlaylistTitle = "YouTube Favorites Updated"
	mapping.SPPlaylistTitle = "Spotify Favorites Updated"
	oldUpdatedAt := mapping.UpdatedAt

	if err := repo.Save(ctx, mapping); err != nil {
		t.Fatalf("Save(update) error = %v", err)
	}
	if !mapping.UpdatedAt.After(oldUpdatedAt) {
		t.Fatalf("Save(update) did not move UpdatedAt forward")
	}

	updated, err := repo.GetByID(ctx, mapping.ID)
	if err != nil {
		t.Fatalf("GetByID(updated) error = %v", err)
	}
	if updated == nil {
		t.Fatalf("GetByID(updated) returned nil mapping")
	}
	if updated.YTPlaylistTitle != mapping.YTPlaylistTitle || updated.SPPlaylistTitle != mapping.SPPlaylistTitle {
		t.Fatalf("updated mapping mismatch: got %+v, want %+v", updated, mapping)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List() len = %d, want 1", len(list))
	}
	if list[0].ID != mapping.ID {
		t.Fatalf("List()[0].ID = %d, want %d", list[0].ID, mapping.ID)
	}

	notFound, err := repo.GetByID(ctx, 99999)
	if err != nil {
		t.Fatalf("GetByID(not-found) error = %v", err)
	}
	if notFound != nil {
		t.Fatalf("GetByID(not-found) = %+v, want nil", notFound)
	}
}

func TestMatchRepositoryCRUD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "yt2sp.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("close db: %v", closeErr)
		}
	})

	repo := NewMatchRepository(db)

	match := &domain.TrackMatch{
		YTVideoID:  "yt-video-1",
		YTTitle:    "Artist - Song",
		SPTrackID:  "sp-track-1",
		SPTitle:    "Song",
		SPArtist:   "Artist",
		Confidence: 0.91,
		Decision:   domain.MatchAuto,
	}

	if err := repo.SaveMatch(ctx, match); err != nil {
		t.Fatalf("SaveMatch(create) error = %v", err)
	}

	stored, err := repo.GetMatch(ctx, match.YTVideoID)
	if err != nil {
		t.Fatalf("GetMatch() error = %v", err)
	}
	if stored == nil {
		t.Fatalf("GetMatch() returned nil")
	}
	if stored.SPTrackID != "sp-track-1" || stored.Decision != domain.MatchAuto {
		t.Fatalf("GetMatch() returned unexpected match: %+v", stored)
	}

	match.SPTrackID = "sp-track-2"
	match.SPTitle = "Song (Remaster)"
	match.Confidence = 0.72
	match.Decision = domain.MatchPending

	if err := repo.SaveMatch(ctx, match); err != nil {
		t.Fatalf("SaveMatch(update) error = %v", err)
	}

	updated, err := repo.GetMatch(ctx, match.YTVideoID)
	if err != nil {
		t.Fatalf("GetMatch(updated) error = %v", err)
	}
	if updated == nil {
		t.Fatalf("GetMatch(updated) returned nil")
	}
	if updated.SPTrackID != match.SPTrackID || updated.SPTitle != match.SPTitle || updated.Decision != match.Decision {
		t.Fatalf("updated match mismatch: got %+v, want %+v", updated, match)
	}

	notFound, err := repo.GetMatch(ctx, "unknown-video")
	if err != nil {
		t.Fatalf("GetMatch(not-found) error = %v", err)
	}
	if notFound != nil {
		t.Fatalf("GetMatch(not-found) = %+v, want nil", notFound)
	}
}

func TestSyncRunEventsTableExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "yt2sp.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("close db: %v", closeErr)
		}
	})

	// Verify table exists after migrations.
	var name string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='sync_run_events'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("sync_run_events table not found: %v", err)
	}
	if name != "sync_run_events" {
		t.Fatalf("sync_run_events table name = %q, want %q", name, "sync_run_events")
	}

	// Verify index on sync_run_id exists.
	var idxName string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_sync_run_events_sync_run_id'`,
	).Scan(&idxName)
	if err != nil {
		t.Fatalf("idx_sync_run_events_sync_run_id index not found: %v", err)
	}

	// Verify SyncRunEvent domain type is usable (compile-time check via assignment).
	_ = domain.SyncRunEvent{
		ID:        1,
		RunID:     2,
		Level:     "info",
		Message:   "test",
		Details:   "optional",
	}
}

func TestSyncRunEventRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "yt2sp.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("close db: %v", closeErr)
		}
	})

	// Set up prerequisite: mapping and sync run.
	mappingRepo := NewMappingRepository(db)
	mapping := &domain.PlaylistMapping{
		YTPlaylistID:    "yt-event-test",
		YTPlaylistTitle: "Event Test Playlist",
		SPPlaylistID:    "sp-event-test",
		SPPlaylistTitle: "Event Test Spotify",
	}
	if err := mappingRepo.Save(ctx, mapping); err != nil {
		t.Fatalf("Save mapping: %v", err)
	}

	run := &domain.SyncRun{
		MappingID: mapping.ID,
		StartedAt: time.Now().UTC(),
		Status:    "running",
	}
	if err := mappingRepo.SaveSyncRun(ctx, run); err != nil {
		t.Fatalf("SaveSyncRun: %v", err)
	}

	eventRepo := NewSyncRunEventRepository(db)

	t.Run("empty run returns empty slice", func(t *testing.T) {
		events, err := eventRepo.ListSyncRunEvents(ctx, run.ID)
		if err != nil {
			t.Fatalf("ListSyncRunEvents(empty) error = %v", err)
		}
		if events == nil {
			t.Fatalf("ListSyncRunEvents(empty) = nil, want empty slice")
		}
		if len(events) != 0 {
			t.Fatalf("ListSyncRunEvents(empty) len = %d, want 0", len(events))
		}
	})

	// Save two events with explicit timestamps (second has earlier time to verify ordering).
	t1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 10, 0, 1, 0, time.UTC)

	ev1 := &domain.SyncRunEvent{
		RunID:     run.ID,
		CreatedAt: t1,
		Level:     "info",
		Message:   "sync started",
		Details:   `{"count":3}`,
	}
	ev2 := &domain.SyncRunEvent{
		RunID:     run.ID,
		CreatedAt: t2,
		Level:     "success",
		Message:   "track matched",
		Details:   `{"track":"Song A"}`,
	}

	if err := eventRepo.SaveSyncRunEvent(ctx, ev1); err != nil {
		t.Fatalf("SaveSyncRunEvent(ev1) error = %v", err)
	}
	if ev1.ID == 0 {
		t.Fatalf("SaveSyncRunEvent(ev1) did not set ID")
	}

	if err := eventRepo.SaveSyncRunEvent(ctx, ev2); err != nil {
		t.Fatalf("SaveSyncRunEvent(ev2) error = %v", err)
	}
	if ev2.ID == 0 {
		t.Fatalf("SaveSyncRunEvent(ev2) did not set ID")
	}
	if ev2.ID == ev1.ID {
		t.Fatalf("SaveSyncRunEvent IDs not unique: both = %d", ev1.ID)
	}

	events, err := eventRepo.ListSyncRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListSyncRunEvents error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListSyncRunEvents len = %d, want 2", len(events))
	}

	// Chronological order: ev1 before ev2.
	if events[0].ID != ev1.ID {
		t.Fatalf("events[0].ID = %d, want %d (ev1)", events[0].ID, ev1.ID)
	}
	if events[1].ID != ev2.ID {
		t.Fatalf("events[1].ID = %d, want %d (ev2)", events[1].ID, ev2.ID)
	}

	// Field round-trip.
	if events[0].RunID != run.ID {
		t.Fatalf("events[0].RunID = %d, want %d", events[0].RunID, run.ID)
	}
	if events[0].Level != "info" {
		t.Fatalf("events[0].Level = %q, want %q", events[0].Level, "info")
	}
	if events[0].Message != "sync started" {
		t.Fatalf("events[0].Message = %q, want %q", events[0].Message, "sync started")
	}
	if events[0].Details != `{"count":3}` {
		t.Fatalf("events[0].Details = %q, want %q", events[0].Details, `{"count":3}`)
	}
	if !events[0].CreatedAt.Equal(t1) {
		t.Fatalf("events[0].CreatedAt = %v, want %v", events[0].CreatedAt, t1)
	}

	if events[1].Level != "success" {
		t.Fatalf("events[1].Level = %q, want %q", events[1].Level, "success")
	}
	if events[1].Details != `{"track":"Song A"}` {
		t.Fatalf("events[1].Details = %q, want %q", events[1].Details, `{"track":"Song A"}`)
	}

	t.Run("zero CreatedAt is set to now", func(t *testing.T) {
		evZero := &domain.SyncRunEvent{
			RunID:   run.ID,
			Level:   "error",
			Message: "something failed",
			// CreatedAt intentionally zero
		}
		before := time.Now().UTC()
		if err := eventRepo.SaveSyncRunEvent(ctx, evZero); err != nil {
			t.Fatalf("SaveSyncRunEvent(zero ts) error = %v", err)
		}
		after := time.Now().UTC()
		if evZero.ID == 0 {
			t.Fatalf("SaveSyncRunEvent(zero ts) did not set ID")
		}
		if evZero.CreatedAt.IsZero() {
			t.Fatalf("SaveSyncRunEvent(zero ts) did not set CreatedAt")
		}
		if evZero.CreatedAt.Before(before) || evZero.CreatedAt.After(after) {
			t.Fatalf("SaveSyncRunEvent(zero ts) CreatedAt = %v, want between %v and %v",
				evZero.CreatedAt, before, after)
		}
	})
}
