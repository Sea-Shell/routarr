package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type migration struct {
	version int
	name    string
	query   string
}

var migrations = []migration{
	{
		version: 1,
		name:    "create_oauth_tokens",
		query: `
CREATE TABLE IF NOT EXISTS oauth_tokens (
	provider TEXT PRIMARY KEY,
	access_token TEXT NOT NULL,
	refresh_token TEXT,
	expiry DATETIME,
	scopes TEXT,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`,
	},
	{
		version: 2,
		name:    "create_playlist_mappings",
		query: `
CREATE TABLE IF NOT EXISTS playlist_mappings (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	youtube_playlist_id TEXT NOT NULL UNIQUE,
	youtube_playlist_title TEXT NOT NULL,
	spotify_playlist_id TEXT NOT NULL UNIQUE,
	spotify_playlist_title TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);`,
	},
	{
		version: 3,
		name:    "create_sync_runs",
		query: `
CREATE TABLE IF NOT EXISTS sync_runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	mapping_id INTEGER NOT NULL,
	started_at DATETIME NOT NULL,
	finished_at DATETIME,
	status TEXT NOT NULL,
	summary TEXT,
	FOREIGN KEY(mapping_id) REFERENCES playlist_mappings(id) ON DELETE CASCADE
);`,
	},
	{
		version: 4,
		name:    "create_track_matches",
		query: `
CREATE TABLE IF NOT EXISTS track_matches (
	youtube_video_id TEXT PRIMARY KEY,
	youtube_title TEXT NOT NULL,
	spotify_track_id TEXT,
	spotify_track_title TEXT,
	spotify_artist TEXT,
	confidence REAL NOT NULL,
	decision TEXT NOT NULL,
	decision_source TEXT,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`,
	},
	{
		version: 5,
		name:    "create_sync_items",
		query: `
CREATE TABLE IF NOT EXISTS sync_items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sync_run_id INTEGER NOT NULL,
	youtube_video_id TEXT NOT NULL,
	selected_spotify_track_id TEXT,
	action TEXT NOT NULL,
	error TEXT,
	FOREIGN KEY(sync_run_id) REFERENCES sync_runs(id) ON DELETE CASCADE,
	FOREIGN KEY(youtube_video_id) REFERENCES track_matches(youtube_video_id)
);`,
	},
	{
		version: 6,
		name:    "create_indexes",
		query: `
CREATE INDEX IF NOT EXISTS idx_sync_runs_mapping_id ON sync_runs(mapping_id);
CREATE INDEX IF NOT EXISTS idx_sync_items_sync_run_id ON sync_items(sync_run_id);
CREATE INDEX IF NOT EXISTS idx_sync_items_youtube_video_id ON sync_items(youtube_video_id);`,
	},
	{
		version: 7,
		name:    "idx_sync_runs_mapping_id_composite",
		query: `
CREATE INDEX IF NOT EXISTS idx_sync_runs_mapping_id ON sync_runs(mapping_id, id);`,
	},
	{
		version: 8,
		name:    "create_track_match_candidates",
		query: `
CREATE TABLE IF NOT EXISTS track_match_candidates (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sync_run_id INTEGER NOT NULL,
	youtube_video_id TEXT NOT NULL,
	spotify_track_id TEXT NOT NULL,
	spotify_title TEXT NOT NULL,
	spotify_artist TEXT NOT NULL,
	confidence REAL NOT NULL,
	rank INTEGER NOT NULL,
	FOREIGN KEY(sync_run_id) REFERENCES sync_runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_tmc_run_video ON track_match_candidates(sync_run_id, youtube_video_id);`,
	},
	{
		version: 9,
		name:    "create_sync_run_events",
		query: `
CREATE TABLE IF NOT EXISTS sync_run_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sync_run_id INTEGER NOT NULL,
	created_at DATETIME NOT NULL,
	level TEXT NOT NULL,
	message TEXT NOT NULL,
	details TEXT NOT NULL DEFAULT '',
	FOREIGN KEY(sync_run_id) REFERENCES sync_runs(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_sync_run_events_sync_run_id ON sync_run_events(sync_run_id);`,
	},
	{
		version: 10,
		name:    "add_synced_tracking_to_track_matches",
		query: `
ALTER TABLE track_matches ADD COLUMN synced_at DATETIME;
ALTER TABLE track_matches ADD COLUMN resync_requested_at DATETIME;
CREATE INDEX IF NOT EXISTS idx_track_matches_synced_at ON track_matches(synced_at);`,
	},
	{
		version: 12,
		name:    "add_schedule_to_playlist_mappings",
		query: `
ALTER TABLE playlist_mappings ADD COLUMN schedule TEXT NOT NULL DEFAULT '';
ALTER TABLE playlist_mappings ADD COLUMN next_scheduled_run DATETIME;`,
	},
	{
		version: 13,
		name:    "create_app_settings",
		query: `
CREATE TABLE IF NOT EXISTS app_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`,
	},
	{
		version: 11,
		name:    "backfill_synced_at_for_existing_adds",
		query: `
UPDATE track_matches
SET synced_at = (
	SELECT MAX(sr.finished_at)
	FROM sync_items si
	JOIN sync_runs sr ON sr.id = si.sync_run_id
	WHERE si.youtube_video_id = track_matches.youtube_video_id
	  AND si.action = 'added'
	  AND sr.status = 'completed'
)
WHERE EXISTS (
	SELECT 1
	FROM sync_items si
	JOIN sync_runs sr ON sr.id = si.sync_run_id
	WHERE si.youtube_video_id = track_matches.youtube_video_id
	  AND si.action = 'added'
	  AND sr.status = 'completed'
);`,
	},
}

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	// Configure connection pool: SQLite allows only one writer at a time.
	// Keep a single connection warm to avoid repeated open/close overhead.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0) // unlimited lifetime

	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Enable WAL mode: allows concurrent reads + one writer
	// (eliminates "database is locked" errors for async goroutines)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	// Set busy timeout: retry for 5 seconds instead of failing immediately on lock
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}

	if err := RunMigrations(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

func RunMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]struct{})
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied migrations: %w", err)
	}

	for _, m := range migrations {
		if _, ok := applied[m.version]; ok {
			continue
		}

		if _, err := tx.ExecContext(ctx, m.query); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations(version, name) VALUES (?, ?)
`, m.version, m.name); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", m.version, m.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}

	return nil
}
