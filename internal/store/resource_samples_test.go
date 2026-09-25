package store

import (
	"testing"
	"time"
)

// TestResourceSamplesCapNewestPerUserIsolation covers the read side of
// resource_samples: the 24h history endpoint is hard-capped (a busy host
// generates tens of thousands of rows a day — more than any chart shows),
// newest rows win, ordering comes back ascending, and non-admins only ever
// see their own rows.
func TestResourceSamplesCapNewestPerUserIsolation(t *testing.T) {
	db := newTestDB(t)

	// Two users sample at the same second; combined the table exceeds the
	// read cap, so the admin query must keep only the newest rows.
	const perUser = maxResourceSamples/2 + 300
	base := time.Now().Add(-6 * time.Hour)
	ts := func(i int) string { return base.Add(time.Duration(i) * time.Second).Format(time.RFC3339) }

	samples := make([]ResourceSample, 0, perUser*2)
	for i := 0; i < perUser; i++ {
		samples = append(samples,
			ResourceSample{UserID: 1, Username: "admin", ContainerID: "a", Container: "a", CreatedAt: ts(i)},
			ResourceSample{UserID: 2, Username: "alice", ContainerID: "b", Container: "b", CreatedAt: ts(i)},
		)
	}
	if err := db.SaveResourceSamples(samples); err != nil {
		t.Fatalf("save samples: %v", err)
	}

	since := time.Now().Add(-24 * time.Hour)
	all, err := db.ResourceSamples(1, true, since)
	if err != nil {
		t.Fatalf("admin query: %v", err)
	}
	if len(all) != maxResourceSamples {
		t.Fatalf("admin rows = %d, want the cap %d", len(all), maxResourceSamples)
	}
	// Ascending order, newest rows kept: the table holds 2*perUser rows, so
	// the cap drops the oldest 2*perUser-maxResourceSamples inserts — i.e. the first
	// (perUser-maxResourceSamples/2) seconds are dropped for both users.
	for i := 1; i < len(all); i++ {
		if all[i-1].CreatedAt > all[i].CreatedAt {
			t.Fatalf("rows not ascending at %d: %s > %s", i, all[i-1].CreatedAt, all[i].CreatedAt)
		}
	}
	droppedSeconds := perUser - maxResourceSamples/2
	if all[0].CreatedAt != ts(droppedSeconds) {
		t.Errorf("oldest kept row = %s, want %s (newest rows must win)", all[0].CreatedAt, ts(droppedSeconds))
	}

	// A regular user sees only their own samples, uncapped at this volume.
	mine, err := db.ResourceSamples(2, false, since)
	if err != nil {
		t.Fatalf("user query: %v", err)
	}
	if len(mine) != perUser {
		t.Fatalf("user rows = %d, want %d", len(mine), perUser)
	}
	for _, s := range mine {
		if s.UserID != 2 {
			t.Fatalf("user query returned userID %d", s.UserID)
		}
	}
}
