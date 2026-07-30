package store

import (
	"testing"
	"time"
)

// TestMCPUsageLog covers recording tool calls, filtering by token/owner, and
// pruning by age — the lifecycle behind a token's LOG dialog.
func TestMCPUsageLog(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateUser("alice", "pw-valid-123", RoleUser, nil, 5, 0); err != nil {
		t.Fatal(err)
	}
	alice, err := db.Authenticate("alice", "pw-valid-123")
	if err != nil {
		t.Fatal(err)
	}

	// Two tool calls on one token.
	db.RecordMCPUsage(MCPUsageLog{
		TokenID: 1, OwnerID: alice.ID, ContainerID: "c1", ContainerName: "dev",
		TokenLabel: "claude", Tool: "read_file", ArgsPreview: `{"path":"/etc/hosts"}`,
	})
	db.RecordMCPUsage(MCPUsageLog{
		TokenID: 1, OwnerID: alice.ID, ContainerID: "c1", ContainerName: "dev",
		TokenLabel: "claude", Tool: "exec_command", ArgsPreview: `{"command":"ls"}`,
	})

	// Filter by token: both rows, newest first.
	rows, err := db.MCPUsageLogs(MCPUsageFilter{TokenID: 1})
	if err != nil || len(rows) != 2 {
		t.Fatalf("MCPUsageLogs token=1: %v (len=%d)", err, len(rows))
	}
	if rows[0].Tool != "exec_command" {
		t.Errorf("expected newest first, got %q", rows[0].Tool)
	}
	if rows[0].Owner != "alice" {
		t.Errorf("owner not joined: %q", rows[0].Owner)
	}
	if rows[0].ContainerName != "dev" || rows[0].TokenLabel != "claude" {
		t.Errorf("denormalized fields lost: %+v", rows[0])
	}

	// Owner scoping: alice's id sees rows; a stranger's id sees none.
	mine, err := db.MCPUsageLogs(MCPUsageFilter{TokenID: 1, OwnerID: alice.ID})
	if err != nil || len(mine) != 2 {
		t.Fatalf("owner-scoped list: %v (len=%d)", err, len(mine))
	}
	other, err := db.MCPUsageLogs(MCPUsageFilter{TokenID: 1, OwnerID: alice.ID + 999})
	if err != nil || len(other) != 0 {
		t.Fatalf("stranger should see no rows: %v (len=%d)", err, len(other))
	}

	// Filter by container.
	byC, err := db.MCPUsageLogs(MCPUsageFilter{ContainerID: "c1"})
	if err != nil || len(byC) != 2 {
		t.Fatalf("container-scoped list: %v (len=%d)", err, len(byC))
	}
	missing, err := db.MCPUsageLogs(MCPUsageFilter{ContainerID: "nope"})
	if err != nil || len(missing) != 0 {
		t.Fatalf("unknown container should be empty: %v (len=%d)", err, len(missing))
	}

	// Limit clamps.
	many, err := db.MCPUsageLogs(MCPUsageFilter{TokenID: 1, Limit: 1})
	if err != nil || len(many) != 1 {
		t.Fatalf("limit=1: %v (len=%d)", err, len(many))
	}
}

// TestMCPUsageLogPrune verifies the 30-day retention deletes only old rows.
func TestMCPUsageLogPrune(t *testing.T) {
	db := newTestDB(t)
	db.RecordMCPUsage(MCPUsageLog{TokenID: 1, ContainerID: "c1", Tool: "old"})
	// Force one row into the past by writing a timestamped row directly.
	old := time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`insert into mcp_usage_logs(token_id, container_id, tool, created_at) values(1,'c1','ancient',?)`, old); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneMCPUsageLogs(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatalf("PruneMCPUsageLogs: %v", err)
	}
	rows, err := db.MCPUsageLogs(MCPUsageFilter{ContainerID: "c1"})
	if err != nil || len(rows) != 1 || rows[0].Tool != "old" {
		t.Fatalf("prune kept wrong rows: %v", rows)
	}
}

// TestMCPAttackLog covers recording rejected requests, filtering, the stats
// summary, and pruning.
func TestMCPAttackLog(t *testing.T) {
	db := newTestDB(t)
	db.RecordMCPAttack(MCPAttackLog{IP: "203.0.113.5", Country: "China", CountryCode: "CN", Reason: "invalid or expired token", Path: "/mcp/badtoken"})
	db.RecordMCPAttack(MCPAttackLog{IP: "203.0.113.5", Country: "China", CountryCode: "CN", Reason: "invalid or expired token", Path: "/mcp/badtoken2"})
	db.RecordMCPAttack(MCPAttackLog{IP: "198.51.100.7", Country: "United States", CountryCode: "US", Reason: "no token supplied", Path: "/mcp/"})

	// All three, newest first.
	all, err := db.MCPAttackLogs(MCPAttackFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("MCPAttackLogs: %v (len=%d)", err, len(all))
	}

	// Filter by IP.
	byIP, err := db.MCPAttackLogs(MCPAttackFilter{IP: "203.0.113"})
	if err != nil || len(byIP) != 2 {
		t.Fatalf("ip filter: %v (len=%d)", err, len(byIP))
	}

	// Free-text on reason.
	byQ, err := db.MCPAttackLogs(MCPAttackFilter{Q: "no token"})
	if err != nil || len(byQ) != 1 {
		t.Fatalf("q filter: %v (len=%d)", err, len(byQ))
	}

	// Stats: 3 total, 2 distinct IPs.
	stats, err := db.MCPAttackStats()
	if err != nil {
		t.Fatalf("MCPAttackStats: %v", err)
	}
	if stats.TotalAttacks != 3 || stats.UniqueIPs != 2 {
		t.Errorf("stats wrong: %+v", stats)
	}
	if len(stats.TopIPs) != 2 || stats.TopIPs[0].Label != "203.0.113.5" || stats.TopIPs[0].Count != 2 {
		t.Errorf("top IPs wrong: %+v", stats.TopIPs)
	}

	// Prune by age.
	past := time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`insert into mcp_attack_logs(ip, reason, created_at) values('203.0.113.9','old',?)`, past); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneMCPAttackLogs(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatalf("PruneMCPAttackLogs: %v", err)
	}
	left, err := db.MCPAttackLogs(MCPAttackFilter{})
	if err != nil || len(left) != 3 {
		t.Fatalf("prune kept wrong rows: %v (len=%d)", left, len(left))
	}
}

// TestMCPMapPoints covers the Security map's data source: successful remote
// MCP access becomes green "access" points, refused requests become yellow/red
// "attack" points, and a repeat source crosses the severity threshold to red.
func TestMCPMapPoints(t *testing.T) {
	db := newTestDB(t)

	// Two successful remote calls from one location → one green access point,
	// count 2.
	for i := 0; i < 2; i++ {
		db.RecordMCPUsage(MCPUsageLog{
			TokenID: 1, ContainerID: "c1", Tool: "read_file",
			IP: "8.8.8.8", Country: "United States", CountryCode: "US", City: "Mountain View",
			Latitude: 37.38, Longitude: -122.07,
		})
	}
	// A LAN call (no geo) must not appear on the map.
	db.RecordMCPUsage(MCPUsageLog{TokenID: 1, ContainerID: "c1", Tool: "exec_command"})

	// A single refused request from one place → yellow (severity 1).
	db.RecordMCPAttack(MCPAttackLog{
		IP: "45.1.1.1", Country: "Netherlands", CountryCode: "NL", City: "Amsterdam",
		Latitude: 52.37, Longitude: 4.89, Reason: "invalid or expired token",
	})
	// Six refused requests from one place → red (severity 2, count ≥ threshold).
	for i := 0; i < 6; i++ {
		db.RecordMCPAttack(MCPAttackLog{
			IP: "203.0.113.5", Country: "China", CountryCode: "CN", City: "Beijing",
			Latitude: 39.90, Longitude: 116.40, Reason: "invalid or expired token",
		})
	}

	points, err := db.MCPMapPoints(500)
	if err != nil {
		t.Fatalf("MCPMapPoints: %v", err)
	}
	// Three distinct locations: 1 access + 2 attack (the no-geo LAN call is absent).
	if len(points) != 3 {
		t.Fatalf("expected 3 map points, got %d: %+v", len(points), points)
	}

	byKind := map[string][]MCPMapPoint{}
	for _, p := range points {
		byKind[p.Kind] = append(byKind[p.Kind], p)
	}
	if len(byKind["access"]) != 1 || byKind["access"][0].Count != 2 || byKind["access"][0].Severity != 0 {
		t.Errorf("access point wrong: %+v", byKind["access"])
	}
	attacks := byKind["attack"]
	if len(attacks) != 2 {
		t.Fatalf("expected 2 attack points, got %d: %+v", len(attacks), attacks)
	}
	// Find the repeat offender (count 6) and the single probe.
	var low, high *MCPMapPoint
	for i := range attacks {
		switch attacks[i].Count {
		case 1:
			low = &attacks[i]
		case 6:
			high = &attacks[i]
		}
	}
	if low == nil || low.Severity != 1 {
		t.Errorf("single probe should be severity 1: %+v", low)
	}
	if high == nil || high.Severity != 2 {
		t.Errorf("repeat offender (6) should be severity 2: %+v", high)
	}
}
