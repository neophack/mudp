package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// envKeyRe matches a plausible env var name: letters/digits/underscore, not
// starting with a digit, e.g. VNC_PW.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ImagePreset captures an admin-defined default configuration for an image. When a user
// picks this image in the create-container modal, the preset auto-fills the form so
// that per-image conventions (e.g. the VNC_PW password env var, the port an app
// listens on, the GPUs it expects) are applied consistently without each user
// re-discovering them. All fields are optional; pointer-typed booleans distinguish
// "not set" (nil) from "explicitly false".
type ImagePreset struct {
	// GPUs selects which host GPUs to expose. Empty/nil leaves the choice to the
	// user; otherwise one of "none", "all", or a comma-separated index list.
	GPUs string `json:"gpus,omitempty"`
	// Env are extra environment variables (KEY=VALUE) to inject, e.g. VNC_PW=secret.
	// A value may embed a {{random_password:N}}, {{random_number:N}}, {{random_string:N}},
	// or {{sequence:N}} placeholder (N optional); ResolveEnvTemplates expands these to
	// a freshly generated value each time a container is built from the image, e.g.
	// VNC_PW={{random_password:16}}. See envgen.go.
	Env []string `json:"env,omitempty"`
	// Ports are container-side ports that should be mapped to the user's allocated
	// host-port range, e.g. ["8080", "8443"]. The host side is auto-assigned.
	Ports []string `json:"ports,omitempty"`
	// Booleans use pointers so the preset can express "leave to user" (nil).
	Forward8080  *bool `json:"forward8080,omitempty"`
	Forward80    *bool `json:"forward80,omitempty"`
	MountNetdisk *bool `json:"mountNetdisk,omitempty"`
	// MountShm controls whether the host's /dev/shm is bind-mounted into the
	// container. Pointer so nil leaves the create form's own default (checked) in
	// place instead of forcing it off.
	MountShm *bool `json:"mountShm,omitempty"`
	// SelectableNetworks is the candidate pool of network names an admin exposes
	// for this image. When set, a user creating a container from this image may
	// only pick networks that are both in this list AND ones they can attach
	// (their own managed networks + group grants + open bridge). Empty/nil means
	// "no restriction" — the user sees every network they can attach, matching the
	// behaviour of an image without a preset. Names are full Docker names.
	SelectableNetworks []string `json:"selectableNetworks,omitempty"`
	// Networks are managed network names pre-checked (attached by default) when a
	// user creates a container from this image. They must be a subset of
	// SelectableNetworks when that pool is non-empty; ValidatePreset enforces it.
	Networks []string `json:"networks,omitempty"`
	// RestartPolicy is one of unless-stopped|always|on-failure|no.
	RestartPolicy string `json:"restartPolicy,omitempty"`
	// Devices are generic --device specs, e.g. /dev/nvidia0 or
	// /dev/foo:/dev/bar:rwm. Used to keep GPUs connected to NVIDIA containers and to
	// pass through arbitrary host devices.
	Devices []string `json:"devices,omitempty"`
	// CDIDevices are CDI device names for the Container Device Interface, e.g.
	// nvidia.com/gpu=0. Requires a CDI-aware Docker runtime.
	CDIDevices []string `json:"cdiDevices,omitempty"`
	// Description is a human-readable note about the image shown to users (what it
	// contains, how to use it). Surfaced in the image list.
	Description string `json:"description,omitempty"`
	// RequireLogin marks this image's forwarded host ports as needing a console
	// login before they can be reached: a browser hitting the port without a
	// valid session is redirected to the login page. It exists for images that
	// ship no authentication of their own (a bare web desktop, a dev server),
	// so an exposed host port is not an open door to the whole LAN. Only TCP/HTTP
	// forwards can honour it — a UDP or raw-TCP forward cannot check a cookie —
	// and the forward layer refuses non-HTTP traffic to one of these ports.
	RequireLogin *bool `json:"requireLogin,omitempty"`
	// NoVNCPasswordEnv names one of this preset's Env keys (e.g. "VNC_PW") whose
	// resolved value should be appended as "?password=" to the container's
	// forwarded-port open link, so clicking it logs straight into a noVNC page
	// instead of stopping at its password prompt. Empty means no auto-login.
	NoVNCPasswordEnv string `json:"novncPasswordEnv,omitempty"`
}

// MarshalJSON serialises the preset to JSON, returning an empty byte slice for a
// zero-value preset so the column stores "" rather than "{}".
func (p *ImagePreset) MarshalJSON() ([]byte, error) {
	type alias ImagePreset
	return json.Marshal((*alias)(p))
}

// EncodePreset marshals a preset to its JSON column value. A nil preset or an empty
// preset yields "" so the stored column stays empty (matching the schema default and
// keeping the frontend's omit-empty behaviour clean).
func EncodePreset(p *ImagePreset) (string, error) {
	if p == nil || isEmptyPreset(p) {
		return "", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodePreset unmarshals a preset from its JSON column value. Empty input yields nil
// (no preset), which the frontend renders as omitted.
func DecodePreset(s string) (*ImagePreset, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var p ImagePreset
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	if isEmptyPreset(&p) {
		return nil, nil
	}
	return &p, nil
}

// isEmptyPreset reports whether every field is at its zero value.
func isEmptyPreset(p *ImagePreset) bool {
	if p == nil {
		return true
	}
	return p.GPUs == "" && len(p.Env) == 0 && len(p.Ports) == 0 &&
		p.Forward8080 == nil && p.Forward80 == nil &&
		p.MountNetdisk == nil && p.MountShm == nil && len(p.Networks) == 0 && len(p.SelectableNetworks) == 0 &&
		p.RestartPolicy == "" &&
		len(p.Devices) == 0 && len(p.CDIDevices) == 0 && p.Description == "" && p.RequireLogin == nil &&
		p.NoVNCPasswordEnv == ""
}

// ValidatePreset performs lightweight, security-conscious validation of an admin
// supplied preset. It rejects malformed env lines, non-numeric ports, and device
// specs that don't look like device paths. It is intentionally permissive about
// device contents (admins own this field) but guards against trivial mistakes.
func ValidatePreset(p *ImagePreset) error {
	if p == nil {
		return nil
	}
	for _, e := range p.Env {
		if !strings.Contains(e, "=") {
			return fmt.Errorf("env entry %q must be KEY=VALUE", e)
		}
	}
	if err := ValidateEnvTemplates(p.Env); err != nil {
		return err
	}
	if p.NoVNCPasswordEnv != "" && !envKeyRe.MatchString(p.NoVNCPasswordEnv) {
		return fmt.Errorf("novnc password env %q must be a valid env var name", p.NoVNCPasswordEnv)
	}
	for _, port := range p.Ports {
		if !isAllDigits(port) {
			return fmt.Errorf("port %q must be a container port number", port)
		}
	}
	switch p.RestartPolicy {
	case "", "no", "always", "on-failure", "unless-stopped":
	default:
		return fmt.Errorf("restart policy %q is not supported", p.RestartPolicy)
	}
	for _, d := range p.Devices {
		if err := validateDeviceSpec(d); err != nil {
			return err
		}
	}
	for _, c := range p.CDIDevices {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("cdi device entry is empty")
		}
	}
	// When an admin has defined a candidate pool of selectable networks, every
	// default network must live inside it — otherwise the default would point at
	// a network the user is not allowed to pick for this image. An empty pool
	// means "no restriction", so the check is skipped and defaults pass through.
	if len(p.SelectableNetworks) > 0 {
		selectable := make(map[string]bool, len(p.SelectableNetworks))
		for _, n := range p.SelectableNetworks {
			selectable[strings.TrimSpace(n)] = true
		}
		for _, d := range p.Networks {
			if !selectable[strings.TrimSpace(d)] {
				return fmt.Errorf("default network %q must be within the selectable networks list", d)
			}
		}
	}
	return nil
}

// isAllDigits reports whether s consists solely of ASCII digits.
func isAllDigits(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validateDeviceSpec checks a --device style spec: host[:container[:rwm]].
func validateDeviceSpec(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return fmt.Errorf("device spec is empty")
	}
	parts := strings.Split(spec, ":")
	if len(parts) < 1 || len(parts) > 3 {
		return fmt.Errorf("device %q must be host[:container[:rwm]]", spec)
	}
	if parts[0] == "" {
		return fmt.Errorf("device %q has no host path", spec)
	}
	return nil
}
