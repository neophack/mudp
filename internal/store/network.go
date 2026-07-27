package store

import (
	"strings"
	"time"
)

// Network grants record which user groups an administrator has allowed to use a
// network they do not own: one of the host's pre-existing Docker networks, or
// another user's mudp-managed network. Membership in a granted group widens what
// a user can see and attach to; it never confers deletion. Only the owner of a
// mudp-managed network (or an admin) may delete it.
//
// Unlike images — where "no group assignment" means public — a network with no
// grants is visible to admins only. Sharing a host network is an explicit
// decision, so it has to be made explicitly.
//
// Docker's "bridge" is the one exception, because it was never mudp's to hand
// out: every user could always attach to it, so with no grants it stays open to
// everyone and the first grant is what narrows it to the chosen groups. See
// dockerx.NetworkAccess, which reads the table with that rule in mind.

// migrateCreateGroupNetworks creates the group→network grant table. Mirrors
// group_images, including the cascade so deleting a group drops its grants.
func migrateCreateGroupNetworks(db executor) error {
	stmts := []string{
		`create table if not exists group_networks (
			group_id integer not null references groups(id) on delete cascade,
			network text not null,
			created_at text not null default '',
			primary key (group_id, network)
		)`,
		`create index if not exists idx_group_networks_network on group_networks(network)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// NetworksForUser returns the full Docker names of the networks granted to any
// group the user belongs to. Used to widen both the Networks list and the
// attachment validation applied on create/update.
func (db *DB) NetworksForUser(userID int64) ([]string, error) {
	rows, err := db.Query(`select distinct gn.network from group_networks gn
		join user_groups ug on ug.group_id = gn.group_id
		where ug.user_id=? order by gn.network`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NetworkGroupNames returns the groups a single network is granted to.
func (db *DB) NetworkGroupNames(network string) []string {
	rows, err := db.Query(`select g.name from groups g join group_networks gn on gn.group_id=g.id
		where gn.network=? order by g.name`, network)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// NetworkGroupsByNetwork returns network name → granted group names for every
// grant, so listing all networks costs one query instead of one per row.
func (db *DB) NetworkGroupsByNetwork() (map[string][]string, error) {
	rows, err := db.Query(`select gn.network, g.name from group_networks gn
		join groups g on g.id = gn.group_id order by gn.network, g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var network, group string
		if err := rows.Scan(&network, &group); err != nil {
			return nil, err
		}
		out[network] = append(out[network], group)
	}
	return out, rows.Err()
}

// SetNetworkGroups replaces the set of groups allowed to use one network. Group
// ids that no longer exist are skipped rather than failing the request, so a
// stale group list in the admin UI can't wedge the dialog.
func (db *DB) SetNetworkGroups(network string, groupIDs []int64) error {
	network = strings.TrimSpace(network)
	if network == "" {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`delete from group_networks where network=?`, network); err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	for _, gid := range groupIDs {
		if _, err := tx.Exec(`insert or ignore into group_networks(group_id, network, created_at)
			select id, ?, ? from groups where id=?`, network, now, gid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteNetworkGroups drops every grant for a network. Called after the network
// itself is removed so the table keeps no rows for names that no longer exist
// (which would otherwise grandfather access in if the name were recreated).
func (db *DB) DeleteNetworkGroups(network string) error {
	_, err := db.Exec(`delete from group_networks where network=?`, network)
	return err
}
