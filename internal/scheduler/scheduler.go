package scheduler

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/bateau84/routarr/internal/domain"
	"github.com/bateau84/routarr/internal/ports"
)

// Scheduler periodically checks mappings with schedules and runs syncs.
type Scheduler struct {
	db          *sql.DB
	mappingRepo ports.MappingRepository
	syncHandler func(ctx context.Context, mapping *domain.PlaylistMapping) error
	ticker      *time.Ticker
	stopCh      chan struct{}
}

// New creates a new Scheduler. The syncHandler callback is called for each
// mapping whose schedule is due. It receives a context that is cancelled on
// shutdown and must be synchronous (the scheduler waits for it to complete).
func New(db *sql.DB, mappingRepo ports.MappingRepository, syncHandler func(ctx context.Context, mapping *domain.PlaylistMapping) error) *Scheduler {
	return &Scheduler{
		db:          db,
		mappingRepo: mappingRepo,
		syncHandler: syncHandler,
		stopCh:      make(chan struct{}),
	}
}

// Start begins the scheduler loop. It checks every 60 seconds.
func (s *Scheduler) Start(ctx context.Context) {
	s.ticker = time.NewTicker(60 * time.Second)
	defer s.ticker.Stop()

	// Run an immediate check on startup.
	s.check(ctx)

	for {
		select {
		case <-s.ticker.C:
			s.check(ctx)
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop signals the scheduler to stop.
func (s *Scheduler) Stop() {
	close(s.stopCh)
}

func (s *Scheduler) check(ctx context.Context) {
	mappings, err := s.mappingRepo.List(ctx)
	if err != nil {
		log.Printf("scheduler: list mappings: %v", err)
		return
	}

	now := time.Now().UTC()
	for i := range mappings {
		m := &mappings[i]
		if m.Schedule == "" {
			continue
		}
		if m.NextScheduledRun != nil && m.NextScheduledRun.After(now) {
			continue
		}

		// Due — run sync.
		log.Printf("scheduler: running sync for mapping %d (%s → %s)", m.ID, m.YTPlaylistTitle, m.SPPlaylistTitle)
		if err := s.syncHandler(ctx, m); err != nil {
			log.Printf("scheduler: mapping %d sync failed: %v", m.ID, err)
		}

		// Compute and persist next run time.
		next := computeNextRun(m.Schedule, now)
		if err := s.setNextRun(ctx, m.ID, next); err != nil {
			log.Printf("scheduler: update next run for mapping %d: %v", m.ID, err)
		} else {
			m.NextScheduledRun = &next
		}
	}
}

func (s *Scheduler) setNextRun(ctx context.Context, mappingID int, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE playlist_mappings
SET next_scheduled_run = ?, updated_at = ?
WHERE id = ?
`, next.UTC(), time.Now().UTC(), mappingID)
	return err
}

// computeNextRun parses the schedule text and computes the next run time.
// Returns a time 1 year out if parsing fails (effectively pauses the schedule).
func computeNextRun(text string, now time.Time) time.Time {
	sched, err := ParseSchedule(text)
	if err != nil {
		log.Printf("scheduler: parse schedule %q: %v", text, err)
		return now.AddDate(1, 0, 0) // pause for a year on parse failure
	}
	return sched.NextAfter(now)
}
