package store

import "testing"

func TestPortForwardConfigDefaultsToOff(t *testing.T) {
	db := newTestDB(t)
	cfg, err := db.PortForwardConfig()
	if err != nil {
		t.Fatalf("PortForwardConfig: %v", err)
	}
	// Forwarding replaces Docker's own publishing, so an untouched install must
	// keep publishing exactly as before.
	if cfg.Enabled() || len(cfg.Networks) != 0 {
		t.Fatalf("PortForwardConfig = %+v, want empty and disabled", cfg)
	}
}

func TestPortForwardConfigRoundTrip(t *testing.T) {
	db := newTestDB(t)
	if err := db.SavePortForwardConfig(PortForwardConfig{Networks: []string{" openwrt-lan ", "", "lab-lan", "OPENWRT-LAN"}}); err != nil {
		t.Fatalf("SavePortForwardConfig: %v", err)
	}
	cfg, err := db.PortForwardConfig()
	if err != nil {
		t.Fatalf("PortForwardConfig: %v", err)
	}
	if len(cfg.Networks) != 2 || cfg.Networks[0] != "openwrt-lan" || cfg.Networks[1] != "lab-lan" {
		t.Fatalf("Networks = %#v, want the trimmed, de-duplicated pair", cfg.Networks)
	}
	if !cfg.Enabled() {
		t.Error("Enabled() = false with two networks configured")
	}
}

func TestSavePortForwardConfigClearsBack(t *testing.T) {
	db := newTestDB(t)
	if err := db.SavePortForwardConfig(PortForwardConfig{Networks: []string{"openwrt-lan"}}); err != nil {
		t.Fatalf("SavePortForwardConfig: %v", err)
	}
	if err := db.SavePortForwardConfig(PortForwardConfig{}); err != nil {
		t.Fatalf("SavePortForwardConfig (clear): %v", err)
	}
	cfg, err := db.PortForwardConfig()
	if err != nil {
		t.Fatalf("PortForwardConfig: %v", err)
	}
	if cfg.Enabled() {
		t.Fatalf("Networks = %#v after clearing, want none", cfg.Networks)
	}
}

func TestManualForwardRoundTrip(t *testing.T) {
	db := newTestDB(t)
	if got, err := db.ManualForwards(); err != nil || len(got) != 0 {
		t.Fatalf("ManualForwards on a fresh server = %v, %v; want empty", got, err)
	}

	saved, err := db.AddManualForward(ManualForward{
		HostPort: 10500, Proto: "TCP", ContainerID: "abc123", TargetPort: 8080,
		Owner: "alice", Note: "vnc", CreatedBy: "admin",
	})
	if err != nil {
		t.Fatalf("AddManualForward: %v", err)
	}
	if saved.ID == 0 || saved.Proto != "tcp" || saved.CreatedAt == "" {
		t.Fatalf("saved = %+v, want an id, a normalised protocol and a timestamp", saved)
	}

	list, err := db.ManualForwards()
	if err != nil || len(list) != 1 {
		t.Fatalf("ManualForwards = %+v, %v; want one entry", list, err)
	}
	if list[0].ContainerID != "abc123" || list[0].TargetPort != 8080 || list[0].Owner != "alice" {
		t.Fatalf("stored forward = %+v", list[0])
	}

	if err := db.DeleteManualForward(saved.ID); err != nil {
		t.Fatalf("DeleteManualForward: %v", err)
	}
	if list, _ := db.ManualForwards(); len(list) != 0 {
		t.Fatalf("ManualForwards after delete = %+v, want empty", list)
	}
	// Deleting again reports that there was nothing to delete, so the console
	// can tell "already gone" from "nothing happened".
	if err := db.DeleteManualForward(saved.ID); err == nil {
		t.Error("deleting a missing forward returned no error")
	}
}

// One socket, one rule: a second forward on the same host port and protocol is
// refused here rather than discovered by a listener that cannot bind.
func TestAddManualForwardRejectsDuplicateSocket(t *testing.T) {
	db := newTestDB(t)
	first := ManualForward{HostPort: 10500, Proto: "tcp", TargetIP: "10.210.1.3", TargetPort: 80}
	if _, err := db.AddManualForward(first); err != nil {
		t.Fatalf("AddManualForward: %v", err)
	}
	if _, err := db.AddManualForward(first); err == nil {
		t.Fatal("a second forward on the same host port was accepted")
	}
	// The same port in the other protocol is a different socket and is allowed.
	udp := first
	udp.Proto = "udp"
	if _, err := db.AddManualForward(udp); err != nil {
		t.Fatalf("udp on the same port rejected: %v", err)
	}
}

func TestAddManualForwardValidates(t *testing.T) {
	db := newTestDB(t)
	cases := map[string]ManualForward{
		"no target":         {HostPort: 10500, Proto: "tcp", TargetPort: 80},
		"bad protocol":      {HostPort: 10500, Proto: "sctp", TargetIP: "10.0.0.1", TargetPort: 80},
		"host port zero":    {Proto: "tcp", TargetIP: "10.0.0.1", TargetPort: 80},
		"host port too big": {HostPort: 70000, Proto: "tcp", TargetIP: "10.0.0.1", TargetPort: 80},
		"target port zero":  {HostPort: 10500, Proto: "tcp", TargetIP: "10.0.0.1"},
	}
	for name, f := range cases {
		if _, err := db.AddManualForward(f); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}
}

func TestParsePortForwardNetworks(t *testing.T) {
	got := ParsePortForwardNetworks("openwrt-lan, lab-lan\nmudp-alice-net-vlan10\n\n openwrt-lan ")
	want := []string{"openwrt-lan", "lab-lan", "mudp-alice-net-vlan10"}
	if len(got) != len(want) {
		t.Fatalf("ParsePortForwardNetworks = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParsePortForwardNetworks[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := ParsePortForwardNetworks("  \n "); len(got) != 0 {
		t.Fatalf("ParsePortForwardNetworks(blank) = %#v, want none", got)
	}
}
