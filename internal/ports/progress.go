package ports

import "context"

// ProgressReporter receives structured progress notifications during a sync run.
// Implementations must be safe to call concurrently.
// Report never returns an error: progress logging failure must not abort a sync.
type ProgressReporter interface {
	Report(ctx context.Context, runID int, level, message string)
}
