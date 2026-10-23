package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Published services have no source repository, webhook, or build history.
// Keep their operational inventory separately from repository build targets.
// Both inventories are reconstructed from the project manifest.
func (s *Store) SyncImageApps(ctx context.Context, project string, apps []App) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM image_apps WHERE project = ?`, project); err != nil {
		return err
	}
	for _, app := range apps {
		data, err := json.Marshal(app)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO image_apps(name, project, spec) VALUES (?, ?, ?)`, app.Name, project, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ImageAppByName(ctx context.Context, name string) (App, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT spec FROM image_apps WHERE name = ?`, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		return App{}, err
	}
	var app App
	err = json.Unmarshal([]byte(raw), &app)
	return app, err
}

func (s *Store) ListImageApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec FROM image_apps ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var app App
		if err := json.Unmarshal([]byte(raw), &app); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}
