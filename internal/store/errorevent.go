package store

import "time"

// ErrorEvent aggregates same-fault occurrences (panics, 5xx responses) the
// way Sentry groups issues: rows are keyed by fingerprint (hash of
// kind+method+path+normalized message), and every recurrence bumps count and
// last_seen instead of inserting a new row. See internal/server/errmon.go.
type ErrorEvent struct {
	ID          int64  `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"`
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Message     string `json:"message"`
	Stack       string `json:"stack,omitempty"`
	Count       int64  `json:"count"`
	FirstSeen   string `json:"firstSeen"`
	LastSeen    string `json:"lastSeen"`
}

func migrateCreateErrorEvents(db executor) error {
	stmts := []string{
		`create table if not exists error_events (
			id integer primary key autoincrement,
			fingerprint text not null unique,
			kind text not null,
			method text not null default '',
			path text not null default '',
			message text not null,
			stack text not null default '',
			count integer not null default 1,
			first_seen text not null,
			last_seen text not null
		)`,
		`create index if not exists idx_error_events_last_seen on error_events(last_seen desc)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// RecordErrorEvent upserts one aggregated occurrence and reports whether this
// was the first time the fingerprint was seen (so callers can notify admins
// once per issue instead of per occurrence).
func (db *DB) RecordErrorEvent(e ErrorEvent) (firstSeen bool, err error) {
	now := time.Now().Format(time.RFC3339)
	if e.FirstSeen == "" {
		e.FirstSeen = now
	}
	_, err = db.Exec(`insert into error_events(fingerprint, kind, method, path, message, stack, count, first_seen, last_seen)
		values(?,?,?,?,?,?,1,?,?)
		on conflict(fingerprint) do update set
			count = count + 1,
			last_seen = excluded.last_seen`,
		e.Fingerprint, e.Kind, e.Method, e.Path, e.Message, e.Stack, e.FirstSeen, now)
	if err != nil {
		return false, err
	}
	var c int64
	err = db.QueryRow(`select count from error_events where fingerprint=?`, e.Fingerprint).Scan(&c)
	if err != nil {
		return false, err
	}
	return c == 1, nil
}

// ListErrorEvents returns the aggregated error events, most recent first.
func (db *DB) ListErrorEvents() ([]ErrorEvent, error) {
	rows, err := db.Query(`select id, fingerprint, kind, method, path, message, stack, count, first_seen, last_seen
		from error_events order by last_seen desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrorEvent
	for rows.Next() {
		var e ErrorEvent
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Kind, &e.Method, &e.Path, &e.Message, &e.Stack, &e.Count, &e.FirstSeen, &e.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteErrorEvent removes one resolved event from the aggregate.
func (db *DB) DeleteErrorEvent(id int64) error {
	_, err := db.Exec(`delete from error_events where id=?`, id)
	return err
}

// ClearErrorEvents empties the aggregate.
func (db *DB) ClearErrorEvents() error {
	_, err := db.Exec(`delete from error_events`)
	return err
}
