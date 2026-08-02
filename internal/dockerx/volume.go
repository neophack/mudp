package dockerx

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/filters"
	volumetypes "github.com/docker/docker/api/types/volume"
)

// Volume is the UI-facing volume record.
type Volume struct {
	Name       string            `json:"name"`     // display name
	FullName   string            `json:"fullName"` // actual Docker volume name
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	SizeMB     float64           `json:"sizeMb"`
	InUse      bool              `json:"inUse"`
	Labels     map[string]string `json:"labels"`
	CreatedAt  string            `json:"createdAt"`
	Owner      string            `json:"owner,omitempty"`
}

// CreateVolumeOptions describes a volume to create.
type CreateVolumeOptions struct {
	Username string
	// Admin allows the caller to set a non-local driver and arbitrary
	// DriverOpts (e.g. bind-mounting a host path via the local driver's
	// "type=none,o=bind,device=..." trick). Non-admin callers are always
	// forced to driver=local with no DriverOpts.
	Admin      bool
	Name       string // display name (without mudp- prefix)
	Driver     string
	DriverOpts map[string]string
	Labels     map[string]string
}

// VolumeFullName builds the mudp-namespaced volume name for a user.
func VolumeFullName(username, name string) string {
	return Prefix + Slug(username) + "-vol-" + Slug(name)
}

// ListVolumes returns volumes visible to the caller. Admins see all
// mudp-managed volumes; others see only their own (by mudp.user label).
func (d *Client) ListVolumes(ctx context.Context, username string, admin bool) ([]Volume, error) {
	args := filters.NewArgs()
	args.Add("label", ManagedLabel+"=true")
	resp, err := d.c.VolumeList(ctx, volumetypes.ListOptions{Filters: args})
	if err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		owner := v.Labels[UserLabel]
		if owner == "" {
			continue // not mudp-managed despite the filter
		}
		if !admin && owner != username {
			continue
		}
		vol := Volume{
			Name:       labelToDisplay(v.Name),
			FullName:   v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Scope:      v.Scope,
			Labels:     v.Labels,
			CreatedAt:  v.CreatedAt,
			Owner:      owner,
		}
		if v.UsageData != nil {
			vol.SizeMB = round2(float64(v.UsageData.Size) / 1024 / 1024)
			vol.InUse = v.UsageData.RefCount > 0
		}
		out = append(out, vol)
	}
	return out, nil
}

// CreateVolume creates a mudp-managed volume owned by username. Non-admin
// callers are confined to the local driver with no DriverOpts: the local
// driver accepts an arbitrary "type=none,o=bind,device=<path>" combination
// that bind-mounts any host path (including "/") into the volume, which the
// volume file browser would then expose for reading and writing.
func (d *Client) CreateVolume(ctx context.Context, opts CreateVolumeOptions) (string, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return "", fmt.Errorf("volume name is required")
	}
	driver := opts.Driver
	if driver == "" {
		driver = "local"
	}
	driverOpts := opts.DriverOpts
	if !opts.Admin {
		if driver != "local" {
			return "", fmt.Errorf("only the local driver is allowed")
		}
		if len(driverOpts) > 0 {
			return "", fmt.Errorf("driver options are not allowed")
		}
	}
	full := VolumeFullName(opts.Username, name)
	labels := map[string]string{
		ManagedLabel: "true",
		UserLabel:    opts.Username,
		NameLabel:    name,
	}
	labels = mergeUserLabels(labels, opts.Labels)
	vol, err := d.c.VolumeCreate(ctx, volumetypes.CreateOptions{
		Name:       full,
		Driver:     driver,
		DriverOpts: driverOpts,
		Labels:     labels,
	})
	if err != nil {
		return "", err
	}
	return vol.Name, nil
}

// VolumeInspect returns the raw Docker volume detail by full name.
func (d *Client) VolumeInspect(ctx context.Context, name string) (volumetypes.Volume, error) {
	return d.c.VolumeInspect(ctx, name)
}

// RemoveVolume removes a mudp-managed volume. The ownership guard ensures only
// the owner (or an admin) can delete it.
func (d *Client) RemoveVolume(ctx context.Context, name string, username string, admin bool, force bool) error {
	vol, err := d.c.VolumeInspect(ctx, name)
	if err != nil {
		return err
	}
	if vol.Labels[ManagedLabel] != "true" {
		return fmt.Errorf("volume %q is not managed by mudp", name)
	}
	if !admin && vol.Labels[UserLabel] != username {
		return fmt.Errorf("volume %q is not yours", name)
	}
	return d.c.VolumeRemove(ctx, name, force)
}

// PruneVolumes removes all unused volumes owned by username (admin: all unused
// mudp volumes). Returns the count reclaimed and bytes freed.
func (d *Client) PruneVolumes(ctx context.Context, username string, admin bool) (int, int64, error) {
	args := filters.NewArgs()
	args.Add("label", ManagedLabel+"=true")
	if !admin {
		args.Add("label", UserLabel+"="+username)
	}
	// Only dangling/unused volumes can be pruned safely.
	args.Add("dangling", "true")
	report, err := d.c.VolumesPrune(ctx, args)
	if err != nil {
		return 0, 0, err
	}
	return len(report.VolumesDeleted), int64(report.SpaceReclaimed), nil
}

// labelToDisplay turns a full mudp volume name back into its display name.
func labelToDisplay(full string) string {
	s := strings.TrimPrefix(full, Prefix)
	// Drop the "<user>-vol-" segment.
	if i := strings.Index(s, "-vol-"); i >= 0 {
		return s[i+len("-vol-"):]
	}
	return full
}
