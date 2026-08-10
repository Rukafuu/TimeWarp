package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
	"github.com/timewarp-dev/timewarp/pkg/consent"
	"github.com/timewarp-dev/timewarp/pkg/protocol"
	_ "modernc.org/sqlite"
)

type SQLite struct{ db *sql.DB }

func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS events (
 trace_id TEXT NOT NULL, event_id TEXT NOT NULL, parent_id TEXT NOT NULL DEFAULT '',
 service TEXT NOT NULL, instance TEXT NOT NULL DEFAULT '', type TEXT NOT NULL,
 timestamp INTEGER NOT NULL, duration_ms INTEGER NOT NULL DEFAULT 0,
 metadata BLOB, http BLOB, PRIMARY KEY(trace_id,event_id));
CREATE INDEX IF NOT EXISTS events_trace_time ON events(trace_id,timestamp,event_id);`)
	if err == nil {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS capability_grants (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL, actor TEXT NOT NULL,
 scopes BLOB NOT NULL, reason TEXT NOT NULL, status TEXT NOT NULL,
 requested_at INTEGER NOT NULL, approved_at INTEGER NOT NULL DEFAULT 0,
 expires_at INTEGER NOT NULL DEFAULT 0, revoked_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS grants_session_status ON capability_grants(session_id,status,expires_at);
CREATE INDEX IF NOT EXISTS grants_requested_at ON capability_grants(requested_at DESC);`)
	}
	if err == nil {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS checkpoints (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL, actor TEXT NOT NULL,
 workspace TEXT NOT NULL, label TEXT NOT NULL, status TEXT NOT NULL,
 created_at INTEGER NOT NULL, reverted_at INTEGER NOT NULL DEFAULT 0,
 safety_checkpoint_id TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS checkpoint_files (
 checkpoint_id TEXT NOT NULL, path TEXT NOT NULL, existed INTEGER NOT NULL,
 mode INTEGER NOT NULL DEFAULT 0, sha256 TEXT NOT NULL DEFAULT '',
 size INTEGER NOT NULL DEFAULT 0, content BLOB,
 PRIMARY KEY(checkpoint_id,path),
 FOREIGN KEY(checkpoint_id) REFERENCES checkpoints(id) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS checkpoints_session_time ON checkpoints(session_id,created_at DESC);`)
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return s, nil
}

func (s *SQLite) CreateCheckpoint(ctx context.Context, item checkpoint.Checkpoint) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints(id,session_id,actor,workspace,label,status,created_at,reverted_at,safety_checkpoint_id) VALUES(?,?,?,?,?,?,?,?,?)`,
		item.ID, item.SessionID, item.Actor, item.Workspace, item.Label, item.Status, item.CreatedAt, item.RevertedAt, item.SafetyCheckpointID); err != nil {
		return err
	}
	for _, file := range item.Files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoint_files(checkpoint_id,path,existed,mode,sha256,size,content) VALUES(?,?,?,?,?,?,?)`,
			item.ID, file.Path, file.Existed, file.Mode, file.SHA256, file.Size, file.Content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) GetCheckpoint(ctx context.Context, id string) (checkpoint.Checkpoint, error) {
	item, err := scanCheckpoint(s.db.QueryRowContext(ctx, `SELECT id,session_id,actor,workspace,label,status,created_at,reverted_at,safety_checkpoint_id FROM checkpoints WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Checkpoint{}, checkpoint.ErrNotFound
	}
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	files, err := s.checkpointFiles(ctx, id)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	item.Files = files
	return item, nil
}

func (s *SQLite) ListCheckpoints(ctx context.Context, filter checkpoint.ListFilter) ([]checkpoint.Checkpoint, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	query := `SELECT id,session_id,actor,workspace,label,status,created_at,reverted_at,safety_checkpoint_id FROM checkpoints`
	args := []any{}
	if filter.SessionID != "" {
		query += ` WHERE session_id=?`
		args = append(args, filter.SessionID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var items []checkpoint.Checkpoint
	for rows.Next() {
		item, err := scanCheckpoint(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Files, err = s.checkpointFiles(ctx, items[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLite) MarkCheckpointReverted(ctx context.Context, id string, revertedAt int64, safetyID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE checkpoints SET status=?,reverted_at=?,safety_checkpoint_id=? WHERE id=? AND status=?`,
		checkpoint.Reverted, revertedAt, safetyID, id, checkpoint.Active)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		if _, getErr := s.GetCheckpoint(ctx, id); errors.Is(getErr, checkpoint.ErrNotFound) {
			return checkpoint.ErrNotFound
		}
		return checkpoint.ErrInvalidState
	}
	return nil
}

type checkpointScanner interface{ Scan(...any) error }

func scanCheckpoint(scanner checkpointScanner) (checkpoint.Checkpoint, error) {
	var item checkpoint.Checkpoint
	err := scanner.Scan(&item.ID, &item.SessionID, &item.Actor, &item.Workspace, &item.Label, &item.Status, &item.CreatedAt, &item.RevertedAt, &item.SafetyCheckpointID)
	return item, err
}

func (s *SQLite) checkpointFiles(ctx context.Context, id string) ([]checkpoint.File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,existed,mode,sha256,size,content FROM checkpoint_files WHERE checkpoint_id=? ORDER BY path`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []checkpoint.File
	for rows.Next() {
		var file checkpoint.File
		if err := rows.Scan(&file.Path, &file.Existed, &file.Mode, &file.SHA256, &file.Size, &file.Content); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *SQLite) CreateGrant(ctx context.Context, grant consent.Grant) error {
	scopes, err := json.Marshal(consent.NormalizeScopes(grant.Scopes))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO capability_grants(id,session_id,actor,scopes,reason,status,requested_at) VALUES(?,?,?,?,?,?,?)`,
		grant.ID, grant.SessionID, grant.Actor, scopes, grant.Reason, consent.Pending, grant.RequestedAt)
	return err
}

func (s *SQLite) GetGrant(ctx context.Context, id string) (consent.Grant, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,session_id,actor,scopes,reason,status,requested_at,approved_at,expires_at,revoked_at FROM capability_grants WHERE id=?`, id)
	grant, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return consent.Grant{}, consent.ErrNotFound
	}
	return grant, err
}

func (s *SQLite) ListGrants(ctx context.Context, filter consent.ListFilter) ([]consent.Grant, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,actor,scopes,reason,status,requested_at,approved_at,expires_at,revoked_at FROM capability_grants ORDER BY requested_at DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := make([]consent.Grant, 0, limit)
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		grant.Status = grant.EffectiveStatus(filter.NowMS)
		if filter.Status != "" && grant.Status != filter.Status {
			continue
		}
		grants = append(grants, grant)
		if len(grants) == limit {
			break
		}
	}
	return grants, rows.Err()
}

func (s *SQLite) ApproveGrant(ctx context.Context, id string, approvedAt, expiresAt int64) (consent.Grant, error) {
	if approvedAt <= 0 || expiresAt <= approvedAt {
		return consent.Grant{}, errors.New("grant expiration must be after approval")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return consent.Grant{}, err
	}
	defer tx.Rollback()
	grant, err := scanGrant(tx.QueryRowContext(ctx, `SELECT id,session_id,actor,scopes,reason,status,requested_at,approved_at,expires_at,revoked_at FROM capability_grants WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return consent.Grant{}, consent.ErrNotFound
	}
	if err != nil {
		return consent.Grant{}, err
	}
	if grant.Status != consent.Pending {
		return consent.Grant{}, consent.ErrInvalidState
	}
	result, err := tx.ExecContext(ctx, `UPDATE capability_grants SET status=?,approved_at=?,expires_at=? WHERE id=? AND status=?`, consent.Active, approvedAt, expiresAt, id, consent.Pending)
	if err != nil {
		return consent.Grant{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return consent.Grant{}, consent.ErrInvalidState
	}
	if err := tx.Commit(); err != nil {
		return consent.Grant{}, err
	}
	grant.Status, grant.ApprovedAt, grant.ExpiresAt = consent.Active, approvedAt, expiresAt
	return grant, nil
}

func (s *SQLite) RevokeGrant(ctx context.Context, id string, revokedAt int64) (consent.Grant, error) {
	grant, err := s.GetGrant(ctx, id)
	if err != nil {
		return consent.Grant{}, err
	}
	if grant.Status == consent.Revoked {
		return consent.Grant{}, consent.ErrInvalidState
	}
	result, err := s.db.ExecContext(ctx, `UPDATE capability_grants SET status=?,revoked_at=? WHERE id=? AND status<>?`, consent.Revoked, revokedAt, id, consent.Revoked)
	if err != nil {
		return consent.Grant{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return consent.Grant{}, consent.ErrInvalidState
	}
	grant.Status, grant.RevokedAt = consent.Revoked, revokedAt
	return grant, nil
}

func (s *SQLite) ActiveGrants(ctx context.Context, sessionID string, nowMS int64) ([]consent.Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,actor,scopes,reason,status,requested_at,approved_at,expires_at,revoked_at FROM capability_grants WHERE session_id=? AND status=? AND expires_at>? ORDER BY expires_at`, sessionID, consent.Active, nowMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []consent.Grant
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

type grantScanner interface{ Scan(...any) error }

func scanGrant(scanner grantScanner) (consent.Grant, error) {
	var grant consent.Grant
	var scopes []byte
	err := scanner.Scan(&grant.ID, &grant.SessionID, &grant.Actor, &scopes, &grant.Reason, &grant.Status, &grant.RequestedAt, &grant.ApprovedAt, &grant.ExpiresAt, &grant.RevokedAt)
	if err != nil {
		return consent.Grant{}, err
	}
	if err := json.Unmarshal(scopes, &grant.Scopes); err != nil {
		return consent.Grant{}, fmt.Errorf("decode grant scopes: %w", err)
	}
	return grant, nil
}

func (s *SQLite) Close() error { return s.db.Close() }
func (s *SQLite) Save(ctx context.Context, e protocol.Event) error {
	return s.SaveBatch(ctx, []protocol.Event{e})
}
func (s *SQLite) SaveBatch(ctx context.Context, events []protocol.Event) error {
	c := ctx
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(c, `INSERT OR IGNORE INTO events(trace_id,event_id,parent_id,service,instance,type,timestamp,duration_ms,metadata,http) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return err
		}
		m, _ := json.Marshal(e.Metadata)
		h, _ := json.Marshal(e.HTTP)
		if _, err = stmt.ExecContext(c, e.TraceID, e.EventID, e.ParentID, e.Service, e.Instance, e.Type, e.Timestamp, e.DurationMS, m, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *SQLite) GetTrace(ctx context.Context, id string) ([]protocol.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,trace_id,parent_id,service,instance,type,timestamp,duration_ms,metadata,http FROM events WHERE trace_id=? ORDER BY timestamp,event_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Event
	for rows.Next() {
		var e protocol.Event
		var m, h []byte
		if err := rows.Scan(&e.EventID, &e.TraceID, &e.ParentID, &e.Service, &e.Instance, &e.Type, &e.Timestamp, &e.DurationMS, &m, &h); err != nil {
			return nil, err
		}
		if len(m) > 0 {
			json.Unmarshal(m, &e.Metadata)
		}
		if string(h) != "null" && len(h) > 0 {
			e.HTTP = &protocol.HTTPRecord{}
			json.Unmarshal(h, e.HTTP)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *SQLite) Search(ctx context.Context, f protocol.TraceFilter) ([]protocol.Trace, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	args := []any{}
	where := ""
	if f.Service != "" {
		where = "WHERE service=?"
		args = append(args, f.Service)
	}
	if !f.Since.IsZero() {
		if where == "" {
			where = "WHERE "
		} else {
			where += " AND "
		}
		where += "timestamp>=?"
		args = append(args, f.Since.UnixMilli())
	}
	args = append(args, limit)
	q := `SELECT trace_id, MIN(timestamp), MAX(timestamp+duration_ms), COUNT(*), MIN(service), MAX(CASE WHEN type IN ('ERROR','TIMEOUT') THEN 1 ELSE 0 END) FROM events ` + where + ` GROUP BY trace_id ORDER BY MIN(timestamp) DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Trace
	for rows.Next() {
		var t protocol.Trace
		var end int64
		var failed int
		if err := rows.Scan(&t.TraceID, &t.StartedAt, &end, &t.EventCount, &t.RootService, &failed); err != nil {
			return nil, err
		}
		t.DurationMS = end - t.StartedAt
		if failed == 1 {
			t.Status = "ERROR"
		} else {
			t.Status = "OK"
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
