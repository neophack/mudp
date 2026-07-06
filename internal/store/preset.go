package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
	Env []string `json:"env,omitempty"`
	// Ports are container-side ports that should be mapped to the user's allocated
	// host-port range, e.g. ["8080", "8443"]. The host side is auto-assigned.
	Ports []string `json:"ports,omitempty"`
	// Booleans use pointers so the preset can express "leave to user" (nil).
	SSH          *bool `json:"ssh,omitempty"`
	VSCode       *bool `json:"vscode,omitempty"`
	Forward8080  *bool `json:"forward8080,omitempty"`
	Forward80    *bool `json:"forward80,omitempty"`
	MountNetdisk *bool `json:"mountNetdisk,omitempty"`
	// MountShm controls whether the host's /dev/shm is bind-mounted into the
	// container. Pointer so nil leaves the create form's own default (checked) in
	// place instead of forcing it off.
	MountShm *bool `json:"mountShm,omitempty"`
	// Networks are managed network names to attach by default.
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
		p.SSH == nil && p.VSCode == nil && p.Forward8080 == nil && p.Forward80 == nil &&
		p.MountNetdisk == nil && p.MountShm == nil && len(p.Networks) == 0 && p.RestartPolicy == "" &&
		len(p.Devices) == 0 && len(p.CDIDevices) == 0 && p.Description == ""
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
