package store

import "testing"

// TestAccessTrendExcludesStaleSameDateRow is a regression test for a format
// mismatch: created_at is stored as RFC3339 ("...T...+08:00"), but
// accessTrend's "last 24h" filter compared it directly against SQLite's
// datetime('now','-24 hours'), which returns a differently-formatted,
// space-separated UTC string ("...  ..."). Because 'T' (0x54) sorts after
// ' ' (0x20), any row whose calendar date matched the cutoff's calendar date
// was judged >= the cutoff regardless of its actual time of day -- so a row
// from earlier that same date, genuinely older than 24h, leaked into the
// trend. Wrapping created_at in datetime(...) makes SQLite parse and
// normalise both sides before comparing.
func TestAccessTrendExcludesStaleSameDateRow(t *testing.T) {
	db := newTestDB(t)

	// Control row: recorded "now" via RecordAccess's own default, always
	// within the last 24h.
	db.RecordAccess(AccessLog{Event: AccessEventPageView, IP: "10.0.0.1"})

	// Derive the exact cutoff instant the production query itself uses, then
	// construct a row that is genuinely BEFORE it (must be excluded) but
	// shares its calendar date -- the precise condition that triggered the
	// bug, independent of the wall-clock time this test happens to run at.
	var cutoff string
	if err := db.QueryRow(`select datetime('now','-24 hours')`).Scan(&cutoff); err != nil {
		t.Fatalf("query cutoff: %v", err)
	}
	if len(cutoff) < 10 {
		t.Fatalf("unexpected cutoff format: %q", cutoff)
	}
	datePart := cutoff[:10] // "YYYY-MM-DD"
	stale := datePart + "T00:00:01+00:00"
	db.RecordAccess(AccessLog{Event: AccessEventPageView, IP: "10.0.0.2", CreatedAt: stale})

	stats, err := db.AccessStats()
	if err != nil {
		t.Fatalf("AccessStats: %v", err)
	}
	if stats.TotalVisits != 2 {
		t.Fatalf("TotalVisits = %d, want 2 (both rows exist)", stats.TotalVisits)
	}
	var trendTotal int
	for _, b := range stats.HourlyTrend {
		trendTotal += b.Count
	}
	if trendTotal != 1 {
		t.Errorf("HourlyTrend total = %d, want 1 (the stale same-date row must be excluded): %+v", trendTotal, stats.HourlyTrend)
	}
}
