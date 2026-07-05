package dockerx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"
)

// Network is the UI-facing network record.
type Network struct {
	Name       string            `json:"name"`
	FullName   string            `json:"fullName"`
	ID         string            `json:"id"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Subnet     string            `json:"subnet"`
	Containers int               `json:"containers"`
	Labels     map[string]string `json:"labels"`
	Owner      string            `json:"owner,omitempty"`
	CreatedAt  string            `json:"createdAt"`
	// System marks Docker's built-in networks (bridge, host, none, etc.) which
	// are shown read-only alongside the user's mudp-managed networks.
	System bool `json:"system,omitempty"`
}

// CreateNetworkOptions describes a network to create.
type CreateNetworkOptions struct {
	Username string
	Name     string
	Driver   string
	Subnet   string
	Labels   map[string]string
}

// NetworkFullName builds the mudp-namespaced network name.
func NetworkFullName(username, name string) string {
	return Prefix + Slug(username) + "-net-" + Slug(name)
}

// ListNetworks returns mudp-managed networks visible to the caller, followed by
// Docker's built-in/system networks (bridge, host, none, …) shown read-only so
// the Networks view is never empty on a fresh host.
func (d *Client) ListNetworks(ctx context.Context, username string, admin bool) ([]Network, error) {
	// Fetch every network once, then partition into managed vs. system.
	nets, err := d.c.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	var managed, system []Network
	for _, n := range nets {
		isManaged := n.Labels[ManagedLabel] == "true"
		owner := n.Labels[UserLabel]
		net := Network{
			Name:       n.Name,
			FullName:   n.Name,
			ID:         n.ID,
			Driver:     n.Driver,
			Scope:      n.Scope,
			Labels:     n.Labels,
			Owner:      owner,
			Containers: len(n.Containers),
		}
		if len(n.IPAM.Config) > 0 {
			net.Subnet = n.IPAM.Config[0].Subnet
		}
		if isManaged {
			if owner == "" {
				continue
			}
			if !admin && owner != username {
				continue
			}
			net.Name = netLabelToDisplay(n.Name)
			if c, ok := n.Labels["mudp.createdAt"]; ok {
				net.CreatedAt = c
			}
			managed = append(managed, net)
		} else if isSystemNetwork(n.Name, n.Driver) {
			// Surface Docker's built-in networks read-only.
			net.System = true
			system = append(system, net)
		}
	}
	// Managed networks first (the user's own), then system defaults.
	return append(managed, system...), nil
}

// isSystemNetwork reports whether a network is one of Docker's built-in
// defaults worth surfacing in the UI. These cannot be deleted via mudp.
func isSystemNetwork(name, driver string) bool {
	return isSystemNetworkName(name)
}

// isSystemNetworkName reports whether a network name is one of Docker's
// built-in defaults (bridge, host, none). Shared by the Networks view and the
// dashboard tile so the two counts agree.
func isSystemNetworkName(name string) bool {
	switch name {
	case "bridge", "host", "none":
		return true
	}
	return false
}

// CreateNetwork creates a mudp-managed network owned by username.
func (d *Client) CreateNetwork(ctx context.Context, opts CreateNetworkOptions) (string, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return "", fmt.Errorf("network name is required")
	}
	driver := opts.Driver
	if driver == "" {
		driver = "bridge"
	}
	full := NetworkFullName(opts.Username, name)
	labels := map[string]string{
		ManagedLabel:   "true",
		UserLabel:      opts.Username,
		NameLabel:      name,
		"mudp.createdAt": time.Now().Format(time.RFC3339),
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	ipam := &network.IPAM{}
	if opts.Subnet != "" {
		ipam.Config = []network.IPAMConfig{{Subnet: opts.Subnet}}
	}
	resp, err := d.c.NetworkCreate(ctx, full, network.CreateOptions{
		Driver: driver,
		IPAM:   ipam,
		Labels: labels,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// RemoveNetwork removes a mudp-managed network with an ownership guard.
func (d *Client) RemoveNetwork(ctx context.Context, name string, username string, admin bool) error {
	info, err := d.c.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return err
	}
	if info.Labels[ManagedLabel] != "true" {
		return fmt.Errorf("network %q is not managed by mudp", name)
	}
	if !admin && info.Labels[UserLabel] != username {
		return fmt.Errorf("network %q is not yours", name)
	}
	if len(info.Containers) > 0 {
		return fmt.Errorf("network %q has %d attached containers; disconnect them first", name, len(info.Containers))
	}
	return d.c.NetworkRemove(ctx, name)
}

// netLabelToDisplay strips the mudp prefix and user segment from a network name.
func netLabelToDisplay(full string) string {
	s := strings.TrimPrefix(full, Prefix)
	if i := strings.Index(s, "-net-"); i >= 0 {
		return s[i+len("-net-"):]
	}
	return full
}
