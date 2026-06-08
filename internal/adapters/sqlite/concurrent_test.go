package sqlite_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bateau84/yt2sp/internal/adapters/sqlite"
	"github.com/bateau84/yt2sp/internal/domain"
)

// TestConcurrentWritesNoDatabaseLocked verifies that WAL mode + busy_timeout
// allow concurrent writes without "database is locked" errors.
// Reproduces the scenario from the async dry-sync handler:
// - Main goroutine: INSERT sync_run + redirect
// - Background goroutine: INSERT sync_run_events (overlapping with status updates)
func TestConcurrentWritesNoDatabaseLocked(t *testing.T) {
	// Create temp db
	f, err := os.CreateTemp("", "yt2sp_concurrent_*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	dbPath := f.Name()
	_ = f.Close()
	defer os.Remove(dbPath)

	// Open with WAL mode configured
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Create repositories
	mappingRepo := sqlite.NewMappingRepository(db)
	eventRepo := sqlite.NewSyncRunEventRepository(db)

	// Create a mapping
	ctx := context.Background()
	mapping := &domain.PlaylistMapping{
		YTPlaylistID:    "yt123",
		YTPlaylistTitle: "Test YT",
		SPPlaylistID:    "sp123",
		SPPlaylistTitle: "Test SP",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := mappingRepo.Save(ctx, mapping); err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	// Create sync run (mimics the main handler creating the run before redirect)
	run := &domain.SyncRun{
		MappingID: mapping.ID,
		StartedAt: time.Now().UTC(),
		Status:    "running",
	}
	if err := mappingRepo.SaveSyncRun(ctx, run); err != nil {
		t.Fatalf("save sync run: %v", err)
	}

	// Now simulate concurrent writes:
	// - Background goroutine: repeatedly INSERT sync_run_events
	// - Main goroutine: UPDATE sync_run status
	// Without WAL mode + busy_timeout, this causes "database is locked" errors
	
	var wg sync.WaitGroup
	errChan := make(chan error, 2)

	// Background goroutine: write events concurrently (mimics async job)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			event := &domain.SyncRunEvent{
				RunID:   run.ID,
				Level:   "info",
				Message: "progress event",
				Details: "",
			}
			if err := eventRepo.SaveSyncRunEvent(ctx, event); err != nil {
				errChan <- err
				return
			}
			time.Sleep(5 * time.Millisecond) // simulate work
		}
	}()

	// Concurrent goroutine: update run status (mimics polling progress page + final status update)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			time.Sleep(10 * time.Millisecond)
			finishedAt := time.Now().UTC()
			if err := mappingRepo.UpdateSyncRunStatus(ctx, run.ID, "completed", finishedAt); err != nil {
				errChan <- err
				return
			}
		}
	}()

	// Wait for both goroutines with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case err := <-errChan:
		t.Fatalf("concurrent write failed: %v (fix did not eliminate race)", err)
	case <-done:
		// Success: no "database is locked" errors
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent write test timed out")
	}

	// Verify data was written correctly
	events, err := eventRepo.ListSyncRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 10 {
		t.Errorf("expected 10 events, got %d", len(events))
	}

	updatedRun, err := mappingRepo.GetSyncRunByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("get sync run: %v", err)
	}
	if updatedRun.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", updatedRun.Status)
	}
}
