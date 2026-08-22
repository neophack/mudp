package store

import "time"

// ProcessWatch is one "notify me when this process exits" registration. The
// watcher goroutine (internal/server/processes.go) polls each watched
// container's `docker top`; when the PID disappears the watch fires and is
// deleted. PID is stored as text because `docker top` reports it as one.
type ProcessWatch struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"userId"`
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
	PID           string `json:"pid"`
	Command       string `json:"command"`
	CreatedAt     string `json:"createdAt"`
}

func migrateCreateProcessWatches(db executor) error {
	if err := execIgnoring(db, `alter table users add column feishu_webhook text not null default ''`, sqliteDuplicateColumn); err != nil {
		return err
	}
	stmts := []string{
		`create table if not exists process_watches (
			id integer primary key autoincrement,
			user_id integer not null,
			container_id text not null,
			container_name text not null default '',
			pid text not null,
			command text not null default '',
			created_at text not null
		)`,
		`create unique index if not exists idx_process_watches_target on process_watches(user_id, container_id, pid)`,
		`create index if not exists idx_process_watches_user on process_watches(user_id)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// AddProcessWatch registers a watch (idempotent per user+container+PID) and
// returns the row's ID. Re-watching an existing target refreshes its command
// and creation time — useful after a container restart reused the PID.
func (db *DB) AddProcessWatch(w ProcessWatch) (int64, error) {
	if w.CreatedAt == "" {
		w.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := db.Exec(`insert into process_watches(user_id, container_id, container_name, pid, command, created_at)
		values(?,?,?,?,?,?)
		on conflict(user_id, container_id, pid) do update set
			container_name=excluded.container_name,
			command=excluded.command,
			created_at=excluded.created_at`,
		w.UserID, w.ContainerID, w.ContainerName, w.PID, w.Command, w.CreatedAt)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`select id from process_watches where user_id=? and container_id=? and pid=?`,
		w.UserID, w.ContainerID, w.PID).Scan(&id)
	return id, err
}

// DeleteProcessWatch removes one of the caller's own watches. Admins may
// remove anyone's (admin=true).
func (db *DB) DeleteProcessWatch(id, userID int64, admin bool) error {
	if admin {
		_, err := db.Exec(`delete from process_watches where id=?`, id)
		return err
	}
	_, err := db.Exec(`delete from process_watches where id=? and user_id=?`, id, userID)
	return err
}

// ProcessWatches returns every registered watch, for the watcher goroutine.
func (db *DB) ProcessWatches() ([]ProcessWatch, error) {
	rows, err := db.Query(`select id, user_id, container_id, container_name, pid, command, created_at from process_watches order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessWatch
	for rows.Next() {
		var w ProcessWatch
		if err := rows.Scan(&w.ID, &w.UserID, &w.ContainerID, &w.ContainerName, &w.PID, &w.Command, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ProcessWatchesForUser returns one user's watches, for the processes page.
func (db *DB) ProcessWatchesForUser(userID int64) ([]ProcessWatch, error) {
	rows, err := db.Query(`select id, user_id, container_id, container_name, pid, command, created_at from process_watches where user_id=? order by created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessWatch
	for rows.Next() {
		var w ProcessWatch
		if err := rows.Scan(&w.ID, &w.UserID, &w.ContainerID, &w.ContainerName, &w.PID, &w.Command, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UserFeishuWebhook returns the user's Feishu custom-bot webhook URL for
// process-exit notifications. Empty when not configured.
func (db *DB) UserFeishuWebhook(userID int64) (string, error) {
	var url string
	err := db.QueryRow(`select feishu_webhook from users where id=?`, userID).Scan(&url)
	return url, err
}

// UpdateUserFeishuWebhook saves (or clears, with "") the user's webhook URL.
func (db *DB) UpdateUserFeishuWebhook(userID int64, url string) error {
	_, err := db.Exec(`update users set feishu_webhook=? where id=?`, url, userID)
	return err
}
