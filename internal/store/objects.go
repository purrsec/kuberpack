package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Objects persist plans, operations and policies. Request keys are unique per
// principal/project, so an uncertain HTTP write can be replayed without work.
type Object struct {
	Kind, ID, Project, Owner, RequestKey string
	Payload                              []byte
}

func (s *Store) CreateObject(ctx context.Context, o Object) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_objects(kind,id,project,owner,request_key,payload,created_at) VALUES(?,?,?,?,?,?,?)`, o.Kind, o.ID, o.Project, o.Owner, o.RequestKey, string(o.Payload), time.Now().UTC().Format(time.RFC3339Nano))
	if isUnique(err) {
		return ErrDuplicate
	}
	return err
}
func (s *Store) SaveObject(ctx context.Context, o Object) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_objects SET payload=? WHERE kind=? AND id=?`, string(o.Payload), o.Kind, o.ID)
	return err
}
func scanObject(row rowScanner) (Object, error) {
	var o Object
	err := row.Scan(&o.Kind, &o.ID, &o.Project, &o.Owner, &o.RequestKey, &o.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}
func (s *Store) Object(ctx context.Context, kind, id string) (Object, error) {
	return scanObject(s.db.QueryRowContext(ctx, `SELECT kind,id,project,owner,request_key,payload FROM api_objects WHERE kind=? AND id=?`, kind, id))
}
func (s *Store) RequestObject(ctx context.Context, kind, project, owner, key string) (Object, error) {
	return scanObject(s.db.QueryRowContext(ctx, `SELECT kind,id,project,owner,request_key,payload FROM api_objects WHERE kind=? AND project=? AND owner=? AND request_key=?`, kind, project, owner, key))
}
func (s *Store) Objects(ctx context.Context, kind, project string) ([]Object, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,id,project,owner,request_key,payload FROM api_objects WHERE kind=? AND (?='' OR project=?) ORDER BY created_at,id`, kind, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Object{}
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *Store) BuildByDelivery(ctx context.Context, appID int64, delivery string) (Build, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM builds WHERE app_id=? AND delivery_id=? ORDER BY id DESC LIMIT 1`, appID, delivery).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, ErrNotFound
	}
	if err != nil {
		return Build{}, err
	}
	return s.BuildByID(ctx, appID, id)
}
func (s *Store) Delivery(ctx context.Context, id string) (Delivery, error) {
	var d Delivery
	err := s.db.QueryRowContext(ctx, `SELECT delivery_id,app_id,event_type,commit_sha,status,error FROM webhook_deliveries WHERE delivery_id=?`, id).Scan(&d.ID, &d.AppID, &d.EventType, &d.CommitSHA, &d.Status, &d.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// A running worker cannot be blindly replayed after a process restart: its Job
// might still exist. Preserve the record and report interruption instead of
// silently creating a second build with the same external side effects.
func (s *Store) RecoverInterruptedBuilds(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	message := "control plane restarted during the build; inspect the existing Job before submitting a new request"
	if _, err := tx.ExecContext(ctx, `UPDATE builds SET status='failed', error=?, finished_at=? WHERE status='running'`, message, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET status='failed', error=? WHERE status='running'`, message); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteObject(ctx context.Context, kind, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_objects WHERE kind=? AND id=?`, kind, id)
	return err
}
func (s *Store) RevokeProjectTokens(ctx context.Context, project string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at=? WHERE project=? AND revoked_at=''`, time.Now().UTC().Format(time.RFC3339Nano), project)
	return err
}
