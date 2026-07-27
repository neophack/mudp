package store

// Port forwarding mode. Publishing a container port is normally Docker's job,
// but on a host where something else owns the firewall — an OpenWrt appliance
// routing a LAN, where Docker's iptables integration is disabled or overwritten
// — `-p 10001:8080` either refuses to bind or ends up bypassed, and the
// published port answers nothing. The container itself is fine: it holds a LAN
// address and `curl 10.210.1.3:8080` works.
//
// So an administrator names the networks where that is the case, and mudp
// forwards those containers' host ports in its own process instead of asking
// Docker to publish them. It is a per-network setting because it describes the
// host's networking, which no user can be expected to know: a container on the
// nominated network is forwarded, everything else keeps Docker's bind.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const portForwardSettingKey = "port_forward"

// PortForwardConfig is the administrator-owned list of networks mudp forwards
// for. Empty means nothing is forwarded and every container publishes through
// Docker exactly as before, which is what an untouched install does.
type PortForwardConfig struct {
	// Networks are network names, either the display name shown on the Networks
	// view ("openwrt-lan") or the full Docker name
	// ("mudp-alice-net-openwrt-lan"). Both forms resolve to the same network.
	Networks []string `json:"networks"`
}

// Enabled reports whether any network is set to forward.
func (c PortForwardConfig) Enabled() bool {
	return len(c.Networks) > 0
}

// PortForwardConfig returns the stored configuration. A server that has never
// been configured reads back empty — forwarding is opt-in, because on a normal
// Docker host publishing is both correct and faster.
func (db *DB) PortForwardConfig() (PortForwardConfig, error) {
	var cfg PortForwardConfig
	raw, err := db.getSetting(portForwardSettingKey)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return PortForwardConfig{}, err
	}
	return NormalizePortForwardConfig(cfg), nil
}

// SavePortForwardConfig replaces the stored configuration.
func (db *DB) SavePortForwardConfig(cfg PortForwardConfig) error {
	raw, err := json.Marshal(NormalizePortForwardConfig(cfg))
	if err != nil {
		return err
	}
	return db.setSetting(portForwardSettingKey, string(raw))
}

// NormalizePortForwardConfig trims the names, drops blanks, and removes
// duplicates (case-insensitively — Docker network names are case-sensitive, but
// an admin typing the same network twice in different cases means it once).
func NormalizePortForwardConfig(cfg PortForwardConfig) PortForwardConfig {
	seen := make(map[string]bool, len(cfg.Networks))
	out := make([]string, 0, len(cfg.Networks))
	for _, n := range cfg.Networks {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	cfg.Networks = out
	return cfg
}

// ManualForward is a forward an administrator added by hand, rather than one
// derived from a container's own published ports. It exists because the
// container-driven rules only cover what mudp created: a service on a network
// mudp does not manage, a port that was never published, or a container an
// admin wants reachable on a second host port all need a rule nobody can infer.
//
// The target is either a container — resolved to its current address on every
// reconcile, so it survives restarts — or a fixed address for anything else on
// the host's networks.
type ManualForward struct {
	ID       int64  `json:"id"`
	HostPort int    `json:"hostPort"`
	Proto    string `json:"proto"`
	// ContainerID names the container to follow. Empty means TargetIP is used.
	ContainerID string `json:"containerId,omitempty"`
	// TargetIP is a fixed address, used when ContainerID is empty.
	TargetIP   string `json:"targetIp,omitempty"`
	TargetPort int    `json:"targetPort"`
	// Owner is the user the forward is attributed to on the admin page. It is a
	// label, not a permission: manual forwards are admin-only either way.
	Owner     string `json:"owner,omitempty"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"createdAt"`
	CreatedBy string `json:"createdBy"`
}

// migrateCreatePortForwards creates the table behind the manual forwards. The
// unique index on (host_port, proto) is what stops two rules from claiming one
// socket — the reconciler would otherwise have to pick a winner every sweep.
func migrateCreatePortForwards(db executor) error {
	stmts := []string{
		`create table if not exists port_forwards (
			id integer primary key autoincrement,
			host_port integer not null,
			proto text not null default 'tcp',
			container_id text not null default '',
			target_ip text not null default '',
			target_port integer not null,
			owner text not null default '',
			note text not null default '',
			created_at text not null default '',
			created_by text not null default ''
		)`,
		`create unique index if not exists idx_port_forwards_socket on port_forwards(host_port, proto)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// ManualForwards returns every hand-added forward, ordered by host port so the
// admin page is stable between polls.
func (db *DB) ManualForwards() ([]ManualForward, error) {
	rows, err := db.Query(`select id, host_port, proto, container_id, target_ip, target_port, owner, note, created_at, created_by
		from port_forwards order by host_port, proto`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManualForward{}
	for rows.Next() {
		var f ManualForward
		if err := rows.Scan(&f.ID, &f.HostPort, &f.Proto, &f.ContainerID, &f.TargetIP, &f.TargetPort, &f.Owner, &f.Note, &f.CreatedAt, &f.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddManualForward stores one forward. The socket is claimed exclusively: a
// second rule on the same host port and protocol is refused here rather than
// discovered later by a listener that cannot bind.
func (db *DB) AddManualForward(f ManualForward) (ManualForward, error) {
	f.Proto = strings.ToLower(strings.TrimSpace(f.Proto))
	if f.Proto == "" {
		f.Proto = "tcp"
	}
	if f.Proto != "tcp" && f.Proto != "udp" {
		return ManualForward{}, fmt.Errorf("protocol must be tcp or udp")
	}
	if f.HostPort < 1 || f.HostPort > 65535 {
		return ManualForward{}, fmt.Errorf("host port must be between 1 and 65535")
	}
	if f.TargetPort < 1 || f.TargetPort > 65535 {
		return ManualForward{}, fmt.Errorf("target port must be between 1 and 65535")
	}
	f.ContainerID = strings.TrimSpace(f.ContainerID)
	f.TargetIP = strings.TrimSpace(f.TargetIP)
	if f.ContainerID == "" && f.TargetIP == "" {
		return ManualForward{}, fmt.Errorf("a target container or address is required")
	}
	f.CreatedAt = time.Now().Format(time.RFC3339)
	res, err := db.Exec(`insert into port_forwards(host_port, proto, container_id, target_ip, target_port, owner, note, created_at, created_by)
		values(?,?,?,?,?,?,?,?,?)`,
		f.HostPort, f.Proto, f.ContainerID, f.TargetIP, f.TargetPort, strings.TrimSpace(f.Owner), strings.TrimSpace(f.Note), f.CreatedAt, f.CreatedBy)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ManualForward{}, fmt.Errorf("host port %d/%s already has a forward", f.HostPort, f.Proto)
		}
		return ManualForward{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ManualForward{}, err
	}
	f.ID = id
	return f, nil
}

// DeleteManualForward removes one forward. A missing id is reported, so the
// console can tell "already gone" from "nothing happened".
func (db *DB) DeleteManualForward(id int64) error {
	res, err := db.Exec(`delete from port_forwards where id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ParsePortForwardNetworks splits an admin's free-text input — one name per
// line, or comma-separated — into names. The console offers checkboxes, but the
// endpoint accepts text so a name can be typed for a network that is not on
// this host yet (a compose stack that has not been brought up).
func ParsePortForwardNetworks(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	return NormalizePortForwardConfig(PortForwardConfig{Networks: fields}).Networks
}
