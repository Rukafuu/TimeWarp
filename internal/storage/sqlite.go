package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

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
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return s, nil
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
