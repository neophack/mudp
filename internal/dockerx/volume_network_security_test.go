package dockerx

import (
	"context"
	"testing"
)

// TestCreateVolumeRejectsBindTrickForNonAdmin ensures a non-admin caller
// cannot use the local driver's "type=none,o=bind,device=/" trick (or any
// other driver) to mount an arbitrary host path through what looks like an
// ordinary named volume. The rejection must happen before any Docker API
// call, so a Client with a nil underlying client is enough to exercise it.
func TestCreateVolumeRejectsBindTrickForNonAdmin(t *testing.T) {
	d := &Client{}
	cases := []CreateVolumeOptions{
		{
			Username:   "alice",
			Name:       "evil",
			DriverOpts: map[string]string{"type": "none", "o": "bind", "device": "/"},
		},
		{
			Username: "alice",
			Name:     "evil2",
			Driver:   "vieux/sshfs",
		},
	}
	for _, opts := range cases {
		if _, err := d.CreateVolume(context.Background(), opts); err == nil {
			t.Errorf("CreateVolume(%+v) for non-admin: expected error, got nil", opts)
		}
	}
}

// TestMergeUserLabelsCannotOverrideOwnership ensures a caller cannot use the
// labels map to forge mudp.user/mudp.managed/mudp.name (or any other mudp.*
// / com.docker.* key), which would let them create a volume or network that
// appears owned by someone else, is invisible to the ownership filter, or
// collides with Docker's reserved label namespace. This is the helper shared
// by CreateContainer, CreateVolume and CreateNetwork.
func TestMergeUserLabelsCannotOverrideOwnership(t *testing.T) {
	base := map[string]string{
		ManagedLabel: "true",
		UserLabel:    "alice",
		NameLabel:    "mine",
	}
	user := map[string]string{
		UserLabel:                    "victim",
		ManagedLabel:                 "false",
		NameLabel:                    "forged",
		"mudp.novnc.password":        "forged",
		"com.docker.compose.project": "forged",
		"team":                       "backend",
	}
	got := mergeUserLabels(base, user)
	if got[UserLabel] != "alice" {
		t.Errorf("UserLabel overridden: got %q, want %q", got[UserLabel], "alice")
	}
	if got[ManagedLabel] != "true" {
		t.Errorf("ManagedLabel overridden: got %q, want %q", got[ManagedLabel], "true")
	}
	if got[NameLabel] != "mine" {
		t.Errorf("NameLabel overridden: got %q, want %q", got[NameLabel], "mine")
	}
	if _, ok := got["com.docker.compose.project"]; ok {
		t.Errorf("com.docker.* label was not dropped: %+v", got)
	}
	if got["team"] != "backend" {
		t.Errorf("legitimate user label was dropped: %+v", got)
	}
}
