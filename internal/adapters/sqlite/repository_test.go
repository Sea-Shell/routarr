package sqlite

import (
	"context"
	"path/filepath"
	"testing"

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
