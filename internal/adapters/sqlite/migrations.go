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
}

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
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
