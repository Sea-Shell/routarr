package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bateau84/routarr/internal/domain"
)

var _ loadSaver = (*SettingsRepository)(nil)

type loadSaver interface {
	Load(context.Context) (*domain.UserSettings, error)
	Save(context.Context, *domain.UserSettings) error
}

type SettingsRepository struct {
	db *sql.DB
}

func NewSettingsRepository(db *sql.DB) *SettingsRepository {
	return &SettingsRepository{db: db}
}

func (r *SettingsRepository) Load(ctx context.Context) (*domain.UserSettings, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM app_settings`)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()

	s := &domain.UserSettings{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		switch k {
		case "time_format":
			s.TimeFormat = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settings: %w", err)
	}
	return s, nil
}

func (r *SettingsRepository) Save(ctx context.Context, s *domain.UserSettings) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin settings tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	upsert := `INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`

	if _, err := tx.ExecContext(ctx, upsert, "time_format", s.TimeFormat); err != nil {
		return fmt.Errorf("save time_format: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit settings: %w", err)
	}
	return nil
}
