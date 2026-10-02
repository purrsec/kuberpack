package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	RootDirectory     string
	BuildCommand      string
	Internet          bool
	Peers             []string
	Secrets           []string
	SecretEnv         string
	InheritSecretFrom string
	WebhookID         int64
	CreatedAt         time.Time
}

type Delivery struct {
	ID          string
	AppID       int64
	EventType   string
	Action      string
	CommitSHA   string
	Received    time.Time
	Status      string
	Error       string
	PullRequest int
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
	ArchiveURL      string
	CreatedAt       time.Time
	FinishedAt      time.Time
}

type Preview struct {
	AppID       int64
	PullRequest int
	HeadSHA     string
	BuildID     int64
	Hostname    string
	Status      string
	UpdatedAt   time.Time
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
  root_directory TEXT NOT NULL DEFAULT '',
  build_command TEXT NOT NULL DEFAULT '',
  secrets TEXT NOT NULL DEFAULT '[]',
  secret_env TEXT NOT NULL DEFAULT 'prod',
  inherit_secret_from TEXT NOT NULL DEFAULT 'dev',
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
  error TEXT NOT NULL DEFAULT '',
  pull_request INTEGER NOT NULL DEFAULT 0
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
  archive_url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS previews (
  app_id INTEGER NOT NULL REFERENCES apps(id),
  pull_request_number INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  build_id INTEGER NOT NULL DEFAULT 0,
  hostname TEXT NOT NULL,
  status TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (app_id, pull_request_number)
);
`)
	if err != nil {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE webhook_deliveries ADD COLUMN pull_request INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE builds ADD COLUMN archive_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE apps ADD COLUMN internet INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE apps ADD COLUMN peers TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE apps ADD COLUMN root_directory TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE apps ADD COLUMN build_command TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE apps ADD COLUMN secrets TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE apps ADD COLUMN secret_env TEXT NOT NULL DEFAULT 'prod'`,
		`ALTER TABLE apps ADD COLUMN inherit_secret_from TEXT NOT NULL DEFAULT 'dev'`,
	} {
		if _, alterErr := s.db.Exec(stmt); alterErr != nil && !strings.Contains(strings.ToLower(alterErr.Error()), "duplicate column") {
			return alterErr
		}
	}
	return nil
}

func (s *Store) InsertApp(ctx context.Context, app App) (App, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	app.ForgejoRepository = strings.ToLower(strings.TrimSpace(app.ForgejoRepository))
	res, err := s.db.ExecContext(ctx, `
INSERT INTO apps (name, forgejo_repository, production_branch, strategy, autodeploy, autodeploy_pr, hostname, port, healthcheck, start_command, root_directory, build_command, internet, peers, secrets, secret_env, inherit_secret_from, webhook_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app.Name, app.ForgejoRepository, app.ProductionBranch, app.Strategy,
		boolInt(app.Autodeploy), boolInt(app.AutodeployPR), app.Hostname, app.Port,
		app.Healthcheck, app.StartCommand, app.RootDirectory, app.BuildCommand,
		boolInt(app.Internet), encodePeers(app.Peers), encodePeers(app.Secrets), app.SecretEnv, app.InheritSecretFrom, app.WebhookID, now,
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

const appSelect = `SELECT id, name, forgejo_repository, production_branch, strategy, autodeploy, autodeploy_pr, hostname, port, healthcheck, start_command, root_directory, build_command, internet, peers, secrets, secret_env, inherit_secret_from, webhook_id, created_at FROM apps`

func scanApp(row *sql.Row) (App, error) {
	var app App
	var created, peers, secrets string
	var auto, autoPR, internet int
	err := row.Scan(&app.ID, &app.Name, &app.ForgejoRepository, &app.ProductionBranch, &app.Strategy, &auto, &autoPR, &app.Hostname, &app.Port, &app.Healthcheck, &app.StartCommand, &app.RootDirectory, &app.BuildCommand, &internet, &peers, &secrets, &app.SecretEnv, &app.InheritSecretFrom, &app.WebhookID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		return App{}, err
	}
	app.Autodeploy = auto != 0
	app.AutodeployPR = autoPR != 0
	app.Internet = internet != 0
	app.Peers = decodePeers(peers)
	app.Secrets = decodePeers(secrets)
	app.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return app, nil
}

func scanAppRow(rows *sql.Rows) (App, error) {
	var app App
	var created, peers, secrets string
	var auto, autoPR, internet int
	err := rows.Scan(&app.ID, &app.Name, &app.ForgejoRepository, &app.ProductionBranch, &app.Strategy, &auto, &autoPR, &app.Hostname, &app.Port, &app.Healthcheck, &app.StartCommand, &app.RootDirectory, &app.BuildCommand, &internet, &peers, &secrets, &app.SecretEnv, &app.InheritSecretFrom, &app.WebhookID, &created)
	if err != nil {
		return App{}, err
	}
	app.Autodeploy = auto != 0
	app.AutodeployPR = autoPR != 0
	app.Internet = internet != 0
	app.Peers = decodePeers(peers)
	app.Secrets = decodePeers(secrets)
	app.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return app, nil
}

func (s *Store) InsertDelivery(ctx context.Context, d Delivery) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO webhook_deliveries (delivery_id, app_id, event_type, action, commit_sha, received_at, status, error, pull_request)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.AppID, d.EventType, d.Action, d.CommitSHA, now, d.Status, d.Error, d.PullRequest,
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
SELECT delivery_id, app_id, event_type, action, commit_sha, received_at, status, error, pull_request
FROM webhook_deliveries WHERE status IN ('queued', 'running') ORDER BY received_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		var received string
		if err := rows.Scan(&d.ID, &d.AppID, &d.EventType, &d.Action, &d.CommitSHA, &received, &d.Status, &d.Error, &d.PullRequest); err != nil {
			return nil, err
		}
		d.Received, _ = time.Parse(time.RFC3339Nano, received)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) SucceededBuildByCommit(ctx context.Context, appID int64, sha string) (Build, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha == "" {
		return Build{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, app_id, delivery_id, environment, commit_sha, image_repository, image_tag, image_digest, infra_commit_sha, status, error, archive_url, created_at, finished_at
FROM builds
WHERE app_id = ? AND status = 'succeeded' AND image_digest != ''
  AND (commit_sha = ? OR commit_sha LIKE ?)
ORDER BY finished_at DESC LIMIT 1`, appID, sha, sha+"%")
	var b Build
	var created, finished string
	err := row.Scan(&b.ID, &b.AppID, &b.DeliveryID, &b.Environment, &b.CommitSHA, &b.ImageRepository, &b.ImageTag, &b.ImageDigest, &b.InfraCommitSHA, &b.Status, &b.Error, &b.ArchiveURL, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, ErrNotFound
	}
	if err != nil {
		return Build{}, err
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	b.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
	return b, nil
}

func (s *Store) InsertBuild(ctx context.Context, b Build) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO builds (app_id, delivery_id, environment, commit_sha, image_repository, image_tag, image_digest, infra_commit_sha, status, error, archive_url, created_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.AppID, b.DeliveryID, b.Environment, b.CommitSHA, b.ImageRepository, b.ImageTag, b.ImageDigest, b.InfraCommitSHA, b.Status, b.Error, b.ArchiveURL, now, "",
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishBuild(ctx context.Context, id int64, b Build) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE builds SET image_repository=?, image_tag=?, image_digest=?, infra_commit_sha=?, status=?, error=?, archive_url=?, finished_at=?
WHERE id=?`,
		b.ImageRepository, b.ImageTag, b.ImageDigest, b.InfraCommitSHA, b.Status, b.Error, b.ArchiveURL, now, id,
	)
	return err
}

func (s *Store) ListBuilds(ctx context.Context, appID int64) ([]Build, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, app_id, delivery_id, environment, commit_sha, image_repository, image_tag, image_digest, infra_commit_sha, status, error, archive_url, created_at, finished_at
FROM builds WHERE app_id = ? ORDER BY id DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Build
	for rows.Next() {
		var b Build
		var created, finished string
		if err := rows.Scan(&b.ID, &b.AppID, &b.DeliveryID, &b.Environment, &b.CommitSHA, &b.ImageRepository, &b.ImageTag, &b.ImageDigest, &b.InfraCommitSHA, &b.Status, &b.Error, &b.ArchiveURL, &created, &finished); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		b.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) LatestProductionBuild(ctx context.Context, appID int64) (Build, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, app_id, delivery_id, environment, commit_sha, image_repository, image_tag, image_digest, infra_commit_sha, status, error, archive_url, created_at, finished_at
FROM builds WHERE app_id = ? AND environment = 'production' ORDER BY id DESC LIMIT 1`, appID)
	var b Build
	var created, finished string
	err := row.Scan(&b.ID, &b.AppID, &b.DeliveryID, &b.Environment, &b.CommitSHA, &b.ImageRepository, &b.ImageTag, &b.ImageDigest, &b.InfraCommitSHA, &b.Status, &b.Error, &b.ArchiveURL, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, ErrNotFound
	}
	if err != nil {
		return Build{}, err
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	b.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
	return b, nil
}

func (s *Store) MarkRolledBack(ctx context.Context, id int64, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE builds SET status = 'rolled_back', error = ?, finished_at = CASE WHEN finished_at = '' THEN ? ELSE finished_at END WHERE id = ?`, errMsg, now, id)
	return err
}

func (s *Store) UpdateApp(ctx context.Context, app App) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE apps SET production_branch=?, strategy=?, autodeploy=?, autodeploy_pr=?, hostname=?, port=?, healthcheck=?, start_command=?, root_directory=?, build_command=?, internet=?, peers=?, secrets=?, secret_env=?, inherit_secret_from=?
WHERE id=?`,
		app.ProductionBranch, app.Strategy, boolInt(app.Autodeploy), boolInt(app.AutodeployPR),
		app.Hostname, app.Port, app.Healthcheck, app.StartCommand, app.RootDirectory, app.BuildCommand,
		boolInt(app.Internet), encodePeers(app.Peers), encodePeers(app.Secrets), app.SecretEnv, app.InheritSecretFrom, app.ID,
	)
	return err
}

func (s *Store) DeleteApp(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM previews WHERE app_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM builds WHERE app_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE app_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpsertPreview(ctx context.Context, p Preview) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO previews (app_id, pull_request_number, head_sha, build_id, hostname, status, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(app_id, pull_request_number) DO UPDATE SET
  head_sha=excluded.head_sha, build_id=excluded.build_id, hostname=excluded.hostname, status=excluded.status, updated_at=excluded.updated_at`,
		p.AppID, p.PullRequest, p.HeadSHA, p.BuildID, p.Hostname, p.Status, now,
	)
	return err
}

func (s *Store) Preview(ctx context.Context, appID int64, pr int) (Preview, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT app_id, pull_request_number, head_sha, build_id, hostname, status, updated_at
FROM previews WHERE app_id = ? AND pull_request_number = ?`, appID, pr)
	var p Preview
	var updated string
	err := row.Scan(&p.AppID, &p.PullRequest, &p.HeadSHA, &p.BuildID, &p.Hostname, &p.Status, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Preview{}, ErrNotFound
	}
	if err != nil {
		return Preview{}, err
	}
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return p, nil
}

func (s *Store) ListOpenPreviews(ctx context.Context) ([]Preview, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT app_id, pull_request_number, head_sha, build_id, hostname, status, updated_at
FROM previews WHERE status NOT IN ('closed', 'pruned')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preview
	for rows.Next() {
		var p Preview
		var updated string
		if err := rows.Scan(&p.AppID, &p.PullRequest, &p.HeadSHA, &p.BuildID, &p.Hostname, &p.Status, &updated); err != nil {
			return nil, err
		}
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

func encodePeers(peers []string) string {
	if peers == nil {
		peers = []string{}
	}
	b, err := json.Marshal(peers)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodePeers(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var peers []string
	if err := json.Unmarshal([]byte(raw), &peers); err != nil {
		return []string{}
	}
	if peers == nil {
		return []string{}
	}
	return peers
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
