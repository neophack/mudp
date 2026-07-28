package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mudp/internal/dockerx"
	"mudp/internal/portfwd"
	"mudp/internal/store"
)

// The forward tests bind real host ports on 127.0.0.1; the ports used here are
// chosen above every user's assigned range so they cannot collide with a
// developer's running containers.

// newForwardApp builds an App with a real store, a live (but empty) port
// forwarder, and a Docker client pointed at nothing. The unreachable daemon is
// deliberate: it keeps the reconcile the settings endpoint runs from touching
// the developer's real containers, and exercises the path where a save succeeds
// while the reconcile reports a problem.
func newForwardApp(t *testing.T) *App {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate("admin", "test-admin-pw"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dc, err := dockerx.NewWithHost("tcp://127.0.0.1:1")
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { dc.Close() })
	m := portfwd.NewManager()
	m.SetLogger(func(format string, args ...any) { t.Logf(format, args...) })
	t.Cleanup(m.Close)
	return &App{db: db, docker: dc, forward: m}
}

// post sends a JSON body to a handler and returns the decoded response.
func post(t *testing.T, h http.HandlerFunc, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	h(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestPortForwardSettingsRoundTrip(t *testing.T) {
	a := newForwardApp(t)

	// A fresh server forwards nothing: Docker publishing is unchanged until an
	// admin says otherwise.
	rec := httptest.NewRecorder()
	a.portForwardSettings(rec, httptest.NewRequest(http.MethodGet, "/api/admin/network/forward", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rec.Code)
	}
	var got struct {
		Networks []string `json:"networks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Networks) != 0 {
		t.Fatalf("networks = %#v on a fresh server, want none", got.Networks)
	}

	// Both input shapes are accepted and merged: the checkbox list and the
	// free-text field the console offers for names not on this host yet.
	code, resp := post(t, a.portForwardSettings, "/api/admin/network/forward",
		`{"networks":["openwrt-lan"],"networksRaw":"lab-lan\nopenwrt-lan"}`)
	if code != http.StatusOK {
		t.Fatalf("POST = %d (%v), want 200", code, resp)
	}
	cfg, err := a.db.PortForwardConfig()
	if err != nil {
		t.Fatalf("PortForwardConfig: %v", err)
	}
	if len(cfg.Networks) != 2 || cfg.Networks[0] != "openwrt-lan" || cfg.Networks[1] != "lab-lan" {
		t.Fatalf("stored networks = %#v, want [openwrt-lan lab-lan]", cfg.Networks)
	}
	if names := a.forwardNetworks(); len(names) != 2 {
		t.Fatalf("forwardNetworks() = %#v, want two entries", names)
	}
}

func TestPortForwardSettingsClears(t *testing.T) {
	a := newForwardApp(t)
	if code, resp := post(t, a.portForwardSettings, "/api/admin/network/forward", `{"networks":["openwrt-lan"]}`); code != http.StatusOK {
		t.Fatalf("POST = %d (%v)", code, resp)
	}
	if code, resp := post(t, a.portForwardSettings, "/api/admin/network/forward", `{"networks":[]}`); code != http.StatusOK {
		t.Fatalf("POST (clear) = %d (%v)", code, resp)
	}
	if names := a.forwardNetworks(); len(names) != 0 {
		t.Fatalf("forwardNetworks() = %#v after clearing, want none", names)
	}
}

func TestPortForwardSettingsRejectsBadMethod(t *testing.T) {
	a := newForwardApp(t)
	rec := httptest.NewRecorder()
	a.portForwardSettings(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/network/forward", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE = %d, want 405", rec.Code)
	}
}

// The user-facing endpoint says which networks forward, with nothing else about
// the host attached to it.
func TestPortForwardInfo(t *testing.T) {
	a := newForwardApp(t)
	if err := a.db.SavePortForwardConfig(store.PortForwardConfig{Networks: []string{"openwrt-lan"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	rec := httptest.NewRecorder()
	a.portForwardInfo(rec, httptest.NewRequest(http.MethodGet, "/api/network/forward", nil))
	var got struct {
		Enabled  bool     `json:"enabled"`
		Networks []string `json:"networks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || len(got.Networks) != 1 || got.Networks[0] != "openwrt-lan" {
		t.Fatalf("info = %+v, want openwrt-lan enabled", got)
	}
}

// The Networks page toggles one network at a time; the result must be the same
// configuration the Settings panel writes.
func TestNetworkForwardToggle(t *testing.T) {
	a := newForwardApp(t)
	admin := &store.User{Username: "admin", Role: store.RoleAdmin}

	// Turning forwarding off for a network that is not on the host must still
	// work — an admin has to be able to clear a stale name.
	code, resp := postAs(t, a, admin, `{"name":"mudp-alice-net-openwrt-lan","forward":false}`)
	if code != http.StatusOK {
		t.Fatalf("toggle off = %d (%v), want 200", code, resp)
	}

	// Seed a configuration that names the network by its display name, then turn
	// it off by its full name: both refer to one network, so nothing may be left
	// behind that would keep forwarding it.
	if err := a.db.SavePortForwardConfig(store.PortForwardConfig{Networks: []string{"openwrt-lan", "lab-lan"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	code, resp = postAs(t, a, admin, `{"name":"mudp-alice-net-openwrt-lan","forward":false}`)
	if code != http.StatusOK {
		t.Fatalf("toggle off = %d (%v), want 200", code, resp)
	}
	cfg, err := a.db.PortForwardConfig()
	if err != nil {
		t.Fatalf("PortForwardConfig: %v", err)
	}
	if len(cfg.Networks) != 1 || cfg.Networks[0] != "lab-lan" {
		t.Fatalf("networks = %#v, want only lab-lan", cfg.Networks)
	}
}

func TestNetworkForwardRequiresAdmin(t *testing.T) {
	a := newForwardApp(t)
	user := &store.User{Username: "bob", Role: store.RoleUser}
	if code, _ := postAs(t, a, user, `{"name":"openwrt-lan","forward":true}`); code != http.StatusForbidden {
		t.Fatalf("toggle as a plain user = %d, want 403", code)
	}
	if names := a.forwardNetworks(); len(names) != 0 {
		t.Fatalf("a non-admin changed the configuration: %#v", names)
	}
}

func TestNetworkForwardRejectsHostAndNone(t *testing.T) {
	a := newForwardApp(t)
	admin := &store.User{Username: "admin", Role: store.RoleAdmin}
	for _, name := range []string{"host", "none"} {
		if code, _ := postAs(t, a, admin, `{"name":"`+name+`","forward":true}`); code != http.StatusBadRequest {
			t.Errorf("toggle %q = %d, want 400", name, code)
		}
	}
	if code, _ := postAs(t, a, admin, `{"forward":true}`); code != http.StatusBadRequest {
		t.Errorf("toggle with no name = %d, want 400", code)
	}
}

// postAs calls the toggle endpoint with a caller in the request context, which
// is what the auth middleware supplies in production.
func postAs(t *testing.T, a *App, u *store.User, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/networks/forward", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey, u))
	a.networkForward(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// A manual forward to a fixed address needs no Docker at all, so it must
// survive an unreachable daemon: it is the rule an admin adds precisely when
// the container-driven ones are not enough.
func TestManualForwardRulesFixedAddress(t *testing.T) {
	a := newForwardApp(t)
	if _, err := a.db.AddManualForward(store.ManualForward{
		HostPort: 10500, Proto: "tcp", TargetIP: "10.210.1.3", TargetPort: 8080, Owner: "alice", Note: "vnc",
	}); err != nil {
		t.Fatalf("AddManualForward: %v", err)
	}
	rules, err := a.manualForwardRules(context.Background())
	if err != nil {
		t.Fatalf("manualForwardRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want one", rules)
	}
	r := rules[0]
	if r.TargetIP != "10.210.1.3" || r.TargetPort != 8080 || r.HostPort != 10500 {
		t.Fatalf("rule = %+v, want 10500 → 10.210.1.3:8080", r)
	}
	if r.Source != "manual" || r.ManualID == 0 || r.Owner != "alice" || r.Name != "vnc" {
		t.Fatalf("rule = %+v, want it attributed to alice and deletable", r)
	}
}

// A manual forward that names a container but cannot resolve it (the daemon is
// down, the container is gone) must still produce a rule, so the page can show
// why it is not listening instead of dropping the row.
func TestManualForwardRulesUnresolvedContainer(t *testing.T) {
	a := newForwardApp(t)
	if _, err := a.db.AddManualForward(store.ManualForward{
		HostPort: 10501, Proto: "tcp", ContainerID: "deadbeefdead", TargetPort: 22,
	}); err != nil {
		t.Fatalf("AddManualForward: %v", err)
	}
	// The Docker client points at nothing, so target resolution fails — but the
	// rule is still produced (with no address) so the page shows the row and the
	// reason, instead of the forward silently vanishing.
	rules, err := a.manualForwardRules(context.Background())
	if err == nil {
		t.Fatal("resolving a container target with no daemon returned no error")
	}
	if len(rules) != 1 || rules[0].HostPort != 10501 || rules[0].TargetIP != "" {
		t.Fatalf("rules = %+v, want one unresolved rule for 10501", rules)
	}
}

// A Docker outage must not tear down forwards whose containers are still
// running: the rules already being served are carried over until the daemon
// answers again, while manual rules are reconciled as usual.
func TestSyncCarriesContainerRulesThroughDockerOutage(t *testing.T) {
	a := newForwardApp(t)
	live := []portfwd.Rule{
		{HostPort: 10500, Proto: "tcp", TargetIP: "127.0.0.1", TargetPort: 9, Source: "container", Name: "dev01"},
		{HostPort: 10501, Proto: "tcp", TargetIP: "127.0.0.1", TargetPort: 9, Source: "manual", ManualID: 7},
	}
	if got := rulesFromSource(live, "container"); len(got) != 1 || got[0].Name != "dev01" {
		t.Fatalf("rulesFromSource(container) = %+v, want only dev01", got)
	}
	if got := rulesFromSource(live, "manual"); len(got) != 1 || got[0].ManualID != 7 {
		t.Fatalf("rulesFromSource(manual) = %+v, want only the manual rule", got)
	}

	// With the daemon unreachable, a sync reports the failure but keeps serving
	// what it had and still brings a new manual rule up.
	if err := a.forward.Apply(live[:1]); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	if _, err := a.db.AddManualForward(store.ManualForward{HostPort: 10502, Proto: "tcp", TargetIP: "127.0.0.1", TargetPort: 9}); err != nil {
		t.Fatalf("AddManualForward: %v", err)
	}
	if err := a.SyncPortForward(context.Background()); err == nil {
		t.Fatal("sync with an unreachable daemon reported no problem")
	}
	ports := map[int]bool{}
	for _, s := range a.forward.Status() {
		ports[s.HostPort] = true
	}
	if !ports[10500] {
		t.Error("the container's forward was torn down while Docker was unreachable")
	}
	if !ports[10502] {
		t.Error("the manual forward did not come up while Docker was unreachable")
	}
}

func TestValidateManualForward(t *testing.T) {
	a := newForwardApp(t)
	a.cfg.Addr = "0.0.0.0:9000"
	ctx := context.Background()

	ok := store.ManualForward{HostPort: 10500, TargetIP: "10.210.1.3", TargetPort: 8080}
	if err := a.validateManualForward(ctx, &ok); err != nil {
		t.Fatalf("valid forward rejected: %v", err)
	}
	if ok.Proto != "tcp" {
		t.Errorf("Proto = %q, want it defaulted to tcp", ok.Proto)
	}

	cases := map[string]store.ManualForward{
		"console's own port": {HostPort: 9000, TargetIP: "10.210.1.3", TargetPort: 8080},
		"no target":          {HostPort: 10500, TargetPort: 8080},
		"bad protocol":       {HostPort: 10500, Proto: "sctp", TargetIP: "10.210.1.3", TargetPort: 8080},
		"target not an ip":   {HostPort: 10500, TargetIP: "not-an-address", TargetPort: 8080},
		"host port too big":  {HostPort: 70000, TargetIP: "10.210.1.3", TargetPort: 8080},
		"target port zero":   {HostPort: 10500, TargetIP: "10.210.1.3"},
	}
	for name, f := range cases {
		f := f
		if err := a.validateManualForward(ctx, &f); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}
}

func TestForwardAddAndDelete(t *testing.T) {
	a := newForwardApp(t)
	rec := httptest.NewRecorder()
	body := `{"hostPort":10500,"proto":"tcp","targetIp":"127.0.0.1","targetPort":9,"note":"discard"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/forwards", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey, &store.User{Username: "admin", Role: store.RoleAdmin}))
	a.forwardAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var added struct {
		Forward store.ManualForward `json:"forward"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if added.Forward.ID == 0 {
		t.Fatalf("response = %s, want the stored forward", rec.Body.String())
	}
	// The rule is live immediately, not only after the next sweep.
	if st := a.forward.Status(); len(st) != 1 || st[0].HostPort != 10500 {
		t.Fatalf("status = %+v, want the new forward listening", st)
	}

	del := httptest.NewRecorder()
	a.forwardDelete(del, httptest.NewRequest(http.MethodPost, "/api/admin/forwards/delete",
		strings.NewReader(`{"id":`+strconv.FormatInt(added.Forward.ID, 10)+`}`)))
	if del.Code != http.StatusOK {
		t.Fatalf("delete = %d (%s), want 200", del.Code, del.Body.String())
	}
	if st := a.forward.Status(); len(st) != 0 {
		t.Fatalf("status = %+v after delete, want the listener stopped", st)
	}
}

func TestForwardDeleteMissing(t *testing.T) {
	a := newForwardApp(t)
	rec := httptest.NewRecorder()
	a.forwardDelete(rec, httptest.NewRequest(http.MethodPost, "/api/admin/forwards/delete", strings.NewReader(`{"id":404}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete of a missing forward = %d, want 400", rec.Code)
	}
}

// forwardNetworks feeds CreateContainer, and it must reflect what the admin
// stored — this is the whole chain from the setting to the network a container
// is actually forwarded on.
func TestForwardNetworksDrivesNetworkSelection(t *testing.T) {
	a := newForwardApp(t)
	if err := a.db.SavePortForwardConfig(store.PortForwardConfig{Networks: []string{"openwrt-lan"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	forwardingOnly := []string{"mudp-alice-net-openwrt-lan"}
	if got := dockerx.ForwardNetworkFor(forwardingOnly, a.forwardNetworks()); got != "mudp-alice-net-openwrt-lan" {
		t.Fatalf("ForwardNetworkFor = %q, want the openwrt-lan network", got)
	}
	// A container that never joins the nominated network keeps Docker publishing.
	if got := dockerx.ForwardNetworkFor([]string{"bridge"}, a.forwardNetworks()); got != "" {
		t.Fatalf("ForwardNetworkFor(bridge only) = %q, want empty", got)
	}
	// A container also joined to bridge can have Docker publish there normally,
	// so the mix keeps Docker publishing instead of forwarding.
	mixed := []string{"bridge", "mudp-alice-net-openwrt-lan"}
	if got := dockerx.ForwardNetworkFor(mixed, a.forwardNetworks()); got != "" {
		t.Fatalf("ForwardNetworkFor(mixed with bridge) = %q, want empty", got)
	}
}
