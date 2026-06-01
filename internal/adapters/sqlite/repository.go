package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bateau84/yt2sp/internal/domain"
	"github.com/bateau84/yt2sp/internal/ports"
)

var _ ports.MappingRepository = (*MappingRepository)(nil)
var _ ports.MatchRepository = (*MatchRepository)(nil)

type MappingRepository struct {
	db *sql.DB
}

func NewMappingRepository(db *sql.DB) *MappingRepository {
	return &MappingRepository{db: db}
}

func (r *MappingRepository) Save(ctx context.Context, m *domain.PlaylistMapping) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("mapping repository is not initialized")
	}
	if m == nil {
		return fmt.Errorf("playlist mapping is nil")
	}

	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now

	if m.ID > 0 {
		res, err := r.db.ExecContext(ctx, `
UPDATE playlist_mappings
SET youtube_playlist_id = ?,
	youtube_playlist_title = ?,
	spotify_playlist_id = ?,
	spotify_playlist_title = ?,
	updated_at = ?
WHERE id = ?
`, m.YTPlaylistID, m.YTPlaylistTitle, m.SPPlaylistID, m.SPPlaylistTitle, m.UpdatedAt, m.ID)
		if err != nil {
			return fmt.Errorf("update playlist mapping: %w", err)
		}

		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("get update affected rows: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("playlist mapping id %d not found", m.ID)
		}

		return nil
	}

	res, err := r.db.ExecContext(ctx, `
INSERT INTO playlist_mappings(
	youtube_playlist_id,
	youtube_playlist_title,
	spotify_playlist_id,
	spotify_playlist_title,
	created_at,
	updated_at
)
VALUES(?, ?, ?, ?, ?, ?)
`, m.YTPlaylistID, m.YTPlaylistTitle, m.SPPlaylistID, m.SPPlaylistTitle, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert playlist mapping: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted mapping id: %w", err)
	}
	m.ID = int(id)

	return nil
}

func (r *MappingRepository) GetByID(ctx context.Context, id int) (*domain.PlaylistMapping, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("mapping repository is not initialized")
	}

	row := r.db.QueryRowContext(ctx, `
SELECT id, youtube_playlist_id, youtube_playlist_title, spotify_playlist_id, spotify_playlist_title, created_at, updated_at
FROM playlist_mappings
WHERE id = ?
`, id)

	var mapping domain.PlaylistMapping
	if err := row.Scan(
		&mapping.ID,
		&mapping.YTPlaylistID,
		&mapping.YTPlaylistTitle,
		&mapping.SPPlaylistID,
		&mapping.SPPlaylistTitle,
		&mapping.CreatedAt,
		&mapping.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get playlist mapping by id: %w", err)
	}

	return &mapping, nil
}

func (r *MappingRepository) List(ctx context.Context) ([]domain.PlaylistMapping, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("mapping repository is not initialized")
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT id, youtube_playlist_id, youtube_playlist_title, spotify_playlist_id, spotify_playlist_title, created_at, updated_at
FROM playlist_mappings
ORDER BY id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list playlist mappings: %w", err)
	}
	defer rows.Close()

	var mappings []domain.PlaylistMapping
	for rows.Next() {
		var mapping domain.PlaylistMapping
		if err := rows.Scan(
			&mapping.ID,
			&mapping.YTPlaylistID,
			&mapping.YTPlaylistTitle,
			&mapping.SPPlaylistID,
			&mapping.SPPlaylistTitle,
			&mapping.CreatedAt,
			&mapping.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan playlist mapping: %w", err)
		}
		mappings = append(mappings, mapping)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate playlist mappings: %w", err)
	}

	return mappings, nil
}

func (r *MappingRepository) GetSyncRunByID(ctx context.Context, runID int) (*domain.SyncRun, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("mapping repository is not initialized")
	}

	row := r.db.QueryRowContext(ctx, `
SELECT id, mapping_id, started_at, finished_at, status
FROM sync_runs
WHERE id = ?
`, runID)

	var run domain.SyncRun
	var finishedAt sql.NullTime
	if err := row.Scan(&run.ID, &run.MappingID, &run.StartedAt, &finishedAt, &run.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get sync run by id: %w", err)
	}

	if finishedAt.Valid {
		run.FinishedAt = &finishedAt.Time
	}

	return &run, nil
}

func (r *MappingRepository) ListSyncRunMatches(ctx context.Context, runID int) ([]domain.TrackMatch, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("mapping repository is not initialized")
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT
	si.youtube_video_id,
	COALESCE(tm.youtube_title, ''),
	COALESCE(tm.spotify_track_id, si.selected_spotify_track_id, ''),
	COALESCE(tm.spotify_track_title, ''),
	COALESCE(tm.spotify_artist, ''),
	COALESCE(tm.confidence, 0),
	COALESCE(tm.decision, '')
FROM sync_items si
LEFT JOIN track_matches tm ON tm.youtube_video_id = si.youtube_video_id
WHERE si.sync_run_id = ?
ORDER BY si.id ASC
`, runID)
	if err != nil {
		return nil, fmt.Errorf("list sync run matches: %w", err)
	}
	defer rows.Close()

	var matches []domain.TrackMatch
	for rows.Next() {
		var m domain.TrackMatch
		var decision string
		if err := rows.Scan(
			&m.YTVideoID,
			&m.YTTitle,
			&m.SPTrackID,
			&m.SPTitle,
			&m.SPArtist,
			&m.Confidence,
			&decision,
		); err != nil {
			return nil, fmt.Errorf("scan sync run match: %w", err)
		}
		m.Decision = domain.MatchDecision(decision)
		matches = append(matches, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sync run matches: %w", err)
	}

	return matches, nil
}

func (r *MappingRepository) UpdateSyncItemAction(ctx context.Context, runID int, ytVideoID, action, itemError string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("mapping repository is not initialized")
	}

	res, err := r.db.ExecContext(ctx, `
UPDATE sync_items
SET action = ?, error = ?
WHERE sync_run_id = ? AND youtube_video_id = ?
`, action, nullableString(itemError), runID, ytVideoID)
	if err != nil {
		return fmt.Errorf("update sync item action: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get sync item action affected rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("sync item run=%d video=%q not found", runID, ytVideoID)
	}

	return nil
}

func (r *MappingRepository) UpdateSyncRunStatus(ctx context.Context, runID int, status string, finishedAt time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("mapping repository is not initialized")
	}

	res, err := r.db.ExecContext(ctx, `
UPDATE sync_runs
SET status = ?, finished_at = ?
WHERE id = ?
`, status, finishedAt, runID)
	if err != nil {
		return fmt.Errorf("update sync run status: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get sync run status affected rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("sync run id %d not found", runID)
	}

	return nil
}

type MatchRepository struct {
	db *sql.DB
}

func NewMatchRepository(db *sql.DB) *MatchRepository {
	return &MatchRepository{db: db}
}

func (r *MatchRepository) SaveMatch(ctx context.Context, m *domain.TrackMatch) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("match repository is not initialized")
	}
	if m == nil {
		return fmt.Errorf("track match is nil")
	}

	_, err := r.db.ExecContext(ctx, `
INSERT INTO track_matches (
	youtube_video_id,
	youtube_title,
	spotify_track_id,
	spotify_track_title,
	spotify_artist,
	confidence,
	decision,
	decision_source,
	created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(youtube_video_id) DO UPDATE SET
	youtube_title = excluded.youtube_title,
	spotify_track_id = excluded.spotify_track_id,
	spotify_track_title = excluded.spotify_track_title,
	spotify_artist = excluded.spotify_artist,
	confidence = excluded.confidence,
	decision = excluded.decision,
	decision_source = excluded.decision_source
`,
		m.YTVideoID,
		m.YTTitle,
		nullableString(m.SPTrackID),
		nullableString(m.SPTitle),
		nullableString(m.SPArtist),
		m.Confidence,
		string(m.Decision),
		"matcher",
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("save track match: %w", err)
	}

	return nil
}

func (r *MatchRepository) GetMatch(ctx context.Context, ytVideoID string) (*domain.TrackMatch, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("match repository is not initialized")
	}

	row := r.db.QueryRowContext(ctx, `
SELECT youtube_video_id, youtube_title, spotify_track_id, spotify_track_title, spotify_artist, confidence, decision
FROM track_matches
WHERE youtube_video_id = ?
`, ytVideoID)

	var match domain.TrackMatch
	var spTrackID sql.NullString
	var spTitle sql.NullString
	var spArtist sql.NullString
	var decision string

	if err := row.Scan(
		&match.YTVideoID,
		&match.YTTitle,
		&spTrackID,
		&spTitle,
		&spArtist,
		&match.Confidence,
		&decision,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, fmt.Errorf("get track match: %w", err)
	}

	match.SPTrackID = spTrackID.String
	match.SPTitle = spTitle.String
	match.SPArtist = spArtist.String
	match.Decision = domain.MatchDecision(decision)

	return &match, nil
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}

	return v
}
