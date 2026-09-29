package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrDuplicate = errors.New("duplicate")

type Store struct {
	db *sql.DB
}

type App struct {
	ID                int64
	Name              string
	ForgejoRepository string
	ProductionBranch  string
	Strategy          string
	Autodeploy        bool
	AutodeployPR      bool
	Hostname          string
	Port              int
	Healthcheck       string
	StartCommand      string
	WebhookID         int64
	CreatedAt         time.Time
}

type Delivery struct {
	ID        string
	AppID     int64
	EventType string
	Action    string
	CommitSHA string
	Received  time.Time
	Status    string
	Error     string
}

type Build struct {
	ID              int64
	AppID           int64
	DeliveryID      string
	Environment     string
	CommitSHA       string
	ImageRepository string
	ImageTag        string
	ImageDigest     string
	InfraCommitSHA  string
	Status          string
	Error           string
	CreatedAt       time.Time
	FinishedAt      time.Time
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." && filepath.Dir(path) != "" {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS apps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  forgejo_repository TEXT NOT NULL UNIQUE,
  production_branch TEXT NOT NULL,
  strategy TEXT NOT NULL DEFAULT 'auto',
  autodeploy INTEGER NOT NULL DEFAULT 1,
  autodeploy_pr INTEGER NOT NULL DEFAULT 0,
  hostname TEXT NOT NULL,
  port INTEGER NOT NULL,
  healthcheck TEXT NOT NULL,
  start_command TEXT NOT NULL DEFAULT '',
  webhook_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS webhook_deliveries (
  delivery_id TEXT PRIMARY KEY,
  app_id INTEGER NOT NULL REFERENCES apps(id),
  event_type TEXT NOT NULL,
  action TEXT NOT NULL DEFAULT '',
  commit_sha TEXT NOT NULL,
  received_at TEXT NOT NULL,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS builds (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id INTEGER NOT NULL REFERENCES apps(id),
  delivery_id TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  image_repository TEXT NOT NULL DEFAULT '',
  image_tag TEXT NOT NULL DEFAULT '',
  image_digest TEXT NOT NULL DEFAULT '',
  infra_commit_sha TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT ''
);
`)
	return err
}

func (s *Store) InsertApp(ctx context.Context, app App) (App, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	app.ForgejoRepository = strings.ToLower(strings.TrimSpace(app.ForgejoRepository))
	res, err := s.db.ExecContext(ctx, `
INSERT INTO apps (name, forgejo_repository, production_branch, strategy, autodeploy, autodeploy_pr, hostname, port, healthcheck, start_command, webhook_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app.Name, app.ForgejoRepository, app.ProductionBranch, app.Strategy,
		boolInt(app.Autodeploy), boolInt(app.AutodeployPR), app.Hostname, app.Port,
		app.Healthcheck, app.StartCommand, app.WebhookID, now,
	)
	if err != nil {
		if isUnique(err) {
			return App{}, fmt.Errorf("%w: app name or repository", ErrDuplicate)
		}
		return App{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return App{}, err
	}
	return s.AppByID(ctx, id)
}

func (s *Store) SetWebhookID(ctx context.Context, id, webhookID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET webhook_id = ? WHERE id = ?`, webhookID, id)
	return err
}

func (s *Store) AppByID(ctx context.Context, id int64) (App, error) {
	return scanApp(s.db.QueryRowContext(ctx, appSelect+` WHERE id = ?`, id))
}

func (s *Store) AppByName(ctx context.Context, name string) (App, error) {
	return scanApp(s.db.QueryRowContext(ctx, appSelect+` WHERE name = ?`, name))
}

func (s *Store) AppByRepository(ctx context.Context, repo string) (App, error) {
	return scanApp(s.db.QueryRowContext(ctx, appSelect+` WHERE forgejo_repository = ?`, strings.ToLower(repo)))
}

func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, appSelect+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		app, err := scanAppRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

const appSelect = `SELECT id, name, forgejo_repository, production_branch, strategy, autodeploy, autodeploy_pr, hostname, port, healthcheck, start_command, webhook_id, created_at FROM apps`

func scanApp(row *sql.Row) (App, error) {
	var app App
	var created string
	var auto, autoPR int
	err := row.Scan(&app.ID, &app.Name, &app.ForgejoRepository, &app.ProductionBranch, &app.Strategy, &auto, &autoPR, &app.Hostname, &app.Port, &app.Healthcheck, &app.StartCommand, &app.WebhookID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		return App{}, err
	}
	app.Autodeploy = auto != 0
	app.AutodeployPR = autoPR != 0
	app.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return app, nil
}

func scanAppRow(rows *sql.Rows) (App, error) {
	var app App
	var created string
	var auto, autoPR int
	err := rows.Scan(&app.ID, &app.Name, &app.ForgejoRepository, &app.ProductionBranch, &app.Strategy, &auto, &autoPR, &app.Hostname, &app.Port, &app.Healthcheck, &app.StartCommand, &app.WebhookID, &created)
	if err != nil {
		return App{}, err
	}
	app.Autodeploy = auto != 0
	app.AutodeployPR = autoPR != 0
	app.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return app, nil
}

func (s *Store) InsertDelivery(ctx context.Context, d Delivery) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO webhook_deliveries (delivery_id, app_id, event_type, action, commit_sha, received_at, status, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.AppID, d.EventType, d.Action, d.CommitSHA, now, d.Status, d.Error,
	)
	if err != nil {
		if isUnique(err) {
			return ErrDuplicate
		}
		return err
	}
	return nil
}

func (s *Store) SetDeliveryStatus(ctx context.Context, id, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries SET status = ?, error = ? WHERE delivery_id = ?`, status, errMsg, id)
	return err
}

func (s *Store) QueuedDeliveries(ctx context.Context) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT delivery_id, app_id, event_type, action, commit_sha, received_at, status, error
FROM webhook_deliveries WHERE status IN ('queued', 'running') ORDER BY received_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		var received string
		if err := rows.Scan(&d.ID, &d.AppID, &d.EventType, &d.Action, &d.CommitSHA, &received, &d.Status, &d.Error); err != nil {
			return nil, err
		}
		d.Received, _ = time.Parse(time.RFC3339Nano, received)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) InsertBuild(ctx context.Context, b Build) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO builds (app_id, delivery_id, environment, commit_sha, image_repository, image_tag, image_digest, infra_commit_sha, status, error, created_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.AppID, b.DeliveryID, b.Environment, b.CommitSHA, b.ImageRepository, b.ImageTag, b.ImageDigest, b.InfraCommitSHA, b.Status, b.Error, now, "",
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishBuild(ctx context.Context, id int64, b Build) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE builds SET image_repository=?, image_tag=?, image_digest=?, infra_commit_sha=?, status=?, error=?, finished_at=?
WHERE id=?`,
		b.ImageRepository, b.ImageTag, b.ImageDigest, b.InfraCommitSHA, b.Status, b.Error, now, id,
	)
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}
