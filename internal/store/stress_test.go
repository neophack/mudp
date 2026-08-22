package store

import (
	"fmt"
	"sync"
	"testing"
)

// TestSQLiteStressMixedLoad hammers one SQLite file from many goroutines with
// the write shapes the server actually produces (audit rows, notifications,
// settings upserts, error-event aggregation) interleaved with reads (user
// list, settings). WAL plus busy_timeout should absorb the contention, so any
// error — "database is locked" in particular — is a failure. Skipped under
// -short so the CI fast path stays quick.
func TestSQLiteStressMixedLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short mode")
	}
	db := newTestDB(t)

	const userCount = 8
	for i := 0; i < userCount; i++ {
		if err := db.CreateUser(fmt.Sprintf("stress%d", i), fmt.Sprintf("stress-pass-%d-Aa9", i), RoleUser, nil, 5, 0); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	users, err := db.Users()
	if err != nil {
		t.Fatalf("Users: %v", err)
	}

	const workers = 6
	const perWorker = 50
	var mu sync.Mutex // guards failureMsgs
	var failureMsgs []string
	record := func(format string, args ...any) {
		mu.Lock()
		failureMsgs = append(failureMsgs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	// fingerprints lets every worker hammer the same few aggregated events, so
	// the upsert path contends on hot rows exactly like parallel 5xx storms do.
	fingerprints := []string{"fp-a", "fp-b", "fp-c"}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				u := users[(w+i)%len(users)]
				switch (w + i) % 5 {
				case 0:
					db.Audit(u.Username, "stress", fmt.Sprintf("w%d-i%d", w, i))
				case 1:
					if err := db.NotifyUser(u.ID, Notification{Type: NotificationSystemAlert, Title: "stress", Message: fmt.Sprintf("w%d-i%d", w, i)}); err != nil {
						record("NotifyUser: %v", err)
					}
				case 2:
					if err := db.SaveSetting("stress_key", fmt.Sprintf("%d-%d", w, i)); err != nil {
						record("SaveSetting: %v", err)
					}
				case 3:
					if _, err := db.Users(); err != nil {
						record("Users: %v", err)
					}
					if _, err := db.Setting("site_name"); err != nil {
						record("Setting: %v", err)
					}
				case 4:
					fp := fingerprints[(w+i)%len(fingerprints)]
					if _, err := db.RecordErrorEvent(ErrorEvent{Fingerprint: fp, Kind: "http", Message: "stress"}); err != nil {
						record("RecordErrorEvent: %v", err)
					}
				}
			}
		}(w)
	}
	wg.Wait()

	for _, msg := range failureMsgs {
		t.Error(msg)
	}
	if len(failureMsgs) > 0 {
		t.FailNow()
	}

	// Post-storm consistency: every write must have landed exactly once.
	assertCount := func(query string, want int64, label string) {
		var got int64
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s count: %v", label, err)
		}
		if got != want {
			t.Errorf("%s count = %d, want %d", label, got, want)
		}
	}
	const total = int64(workers * perWorker)
	assertCount(`select count(*) from audit_logs where action='stress'`, total/5, "audit")
	assertCount(`select count(*) from notifications where title='stress'`, total/5, "notifications")
	assertCount(`select count(*) from error_events`, int64(len(fingerprints)), "error events (aggregated)")
}
