package store

import "time"

// FeishuMessage is one attempted bot message to a user, kept so the processes
// page can show a delivery history with success/failure per message.
type FeishuMessage struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"userId"`
	Kind      string `json:"kind"`
	OpenID    string `json:"openId"`
	Message   string `json:"message"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// Send statuses and message kinds.
const (
	FeishuMessageSent      = "sent"
	FeishuMessageFailed    = "failed"
	FeishuKindProcessWatch = "process_watch"
	FeishuKindAdminTest    = "admin_test"
)

func migrateCreateFeishuMessages(db executor) error {
	stmts := []string{
		`create table if not exists feishu_messages (
			id integer primary key autoincrement,
			user_id integer not null,
			kind text not null default '',
			open_id text not null default '',
			message text not null default '',
			status text not null default '',
			error text not null default '',
			created_at text not null
		)`,
		`create index if not exists idx_feishu_messages_user on feishu_messages(user_id)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// AddFeishuMessage records one send attempt (sent or failed).
func (db *DB) AddFeishuMessage(m FeishuMessage) error {
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := db.Exec(`insert into feishu_messages(user_id, kind, open_id, message, status, error, created_at)
		values(?,?,?,?,?,?,?)`,
		m.UserID, m.Kind, m.OpenID, m.Message, m.Status, m.Error, m.CreatedAt)
	return err
}

// FeishuMessagesForUser returns the user's most recent send attempts.
func (db *DB) FeishuMessagesForUser(userID int64, limit int) ([]FeishuMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(`select id, user_id, kind, open_id, message, status, error, created_at
		from feishu_messages where user_id=? order by id desc limit ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeishuMessage{}
	for rows.Next() {
		var m FeishuMessage
		if err := rows.Scan(&m.ID, &m.UserID, &m.Kind, &m.OpenID, &m.Message, &m.Status, &m.Error, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ClearFeishuMessages deletes all of one user's send history.
func (db *DB) ClearFeishuMessages(userID int64) error {
	_, err := db.Exec(`delete from feishu_messages where user_id=?`, userID)
	return err
}

// PruneFeishuMessages deletes send-history rows older than the cutoff, so the
// table retains at most the last 7 days. Called from the daily maintenance
// pass (see resources.go).
func (db *DB) PruneFeishuMessages(before time.Time) error {
	_, err := db.Exec(`delete from feishu_messages where created_at < ?`, before.Format(time.RFC3339))
	return err
}
