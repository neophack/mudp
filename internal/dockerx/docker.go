package dockerx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const Prefix = "mudp-"

// Label keys stamped on every mudp-managed Docker resource (containers,
// volumes, networks). Centralised so backend + UI agree on the vocabulary.
const (
	ManagedLabel = "mudp.managed"
	UserLabel    = "mudp.user"
	NameLabel    = "mudp.name"
	// NoVNCPassword8080Label stores the resolved value of the env var an image
	// preset named as its 8080 auto-login secret (e.g. VNC_PW), so the
	// container list can build an open link without a per-row inspect call.
	// Empty/absent means the image did not request this.
	NoVNCPassword8080Label = "mudp.novnc.password8080"
	// NoVNCParam8080Label stores the query parameter name that
	// NoVNCPassword8080Label's value is appended as (e.g. "password" yields
	// "?password=<value>"). Empty/absent falls back to DefaultNoVNCParam8080.
	NoVNCParam8080Label = "mudp.novnc.param8080"
	// NoVNCPassword8090Label stores the resolved value of the env var an image
	// preset named as its 8090 auto-login secret (e.g. JUPYTER_TOKEN).
	// Independent of NoVNCPassword8080Label — 8080 and 8090 commonly run
	// different services with different auto-login conventions. Empty/absent
	// means the image did not request this.
	NoVNCPassword8090Label = "mudp.novnc.tkn8090"
	// NoVNCParam8090Label stores the query parameter name that
	// NoVNCPassword8090Label's value is appended as. Empty/absent falls back
	// to DefaultNoVNCParam8090.
	NoVNCParam8090Label = "mudp.novnc.param8090"
)

// DefaultNoVNCParam8080/DefaultNoVNCParam8090 are the query parameter names
// used to auto-login the 8080/8090 forwarded ports when an image preset
// names an auto-login env var but leaves the parameter name unset — noVNC's
// own "?password=" convention for 8080, a token-style "?tkn=" for 8090.
const (
	DefaultNoVNCParam8080 = "password"
	DefaultNoVNCParam8090 = "tkn"
)

var cleanPart = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// mergeUserLabels merges caller-supplied labels on top of the managed
// (mudp.*) labels already present in base, but silently drops any key
// prefixed "mudp." or "com.docker." so a caller can never forge ownership
// (mudp.user), management (mudp.managed) or namespacing (mudp.name) labels,
// nor collide with Docker's own reserved namespace. Used by every resource
// creation path (containers, volumes, networks) that accepts user labels.
func mergeUserLabels(base map[string]string, user map[string]string) map[string]string {
	for k, v := range user {
		k = strings.TrimSpace(k)
		if k == "" || strings.HasPrefix(k, "mudp.") || strings.HasPrefix(k, "com.docker.") {
			continue
		}
		base[k] = v
	}
	return base
}

type Client struct {
	c *client.Client
}

type Container struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	FullName         string            `json:"fullName"`
	Owner            string            `json:"owner,omitempty"`
	Image            string            `json:"image"`
	State            string            `json:"state"`
	Status           string            `json:"status"`
	Ports            []string          `json:"ports"`
	Labels           map[string]string `json:"labels"`
	MemoryMB         float64           `json:"memoryMb"`
	DiskMB           float64           `json:"diskMb"`
	GPU              string            `json:"gpu"`
	GPUPercent       float64           `json:"gpuPct"`
	GPUMemoryMB      float64           `json:"gpuMemMb"`
	GPUMemoryTotalMB float64           `json:"gpuMemTotalMb"`
	GPUMemoryPct     float64           `json:"gpuMemPct"`
	// PortLinks are the clickable access URLs for the container's reachable TCP
	// ports — one link per container port. A port with an HTTPS-terminated
	// forward contributes only its https:// link; its http:// one is omitted.
	PortLinks []PortLink `json:"portLinks,omitempty"`
	CreatedAt int64      `json:"createdAt"`
	// Forwarded reports whether mudp relays this container's host ports itself
	// rather than Docker publishing them — from its forward label, or adopted
	// because it currently sits on a network the administrator marked for
	// forwarding. Drives the "forward" badge in the UI.
	Forwarded bool `json:"forwarded,omitempty"`
}

// PortLink is one clickable host-side access URL for a container TCP port.
type PortLink struct {
	Port int    `json:"port"`
	URL  string `json:"url"`
	// TLS marks an https:// link served by mudp's TLS-terminated forward.
	TLS bool `json:"tls,omitempty"`
}

// SharedDiskMount is one per-user subfolder of a shared disk (共享盘) pool to
// bind into a container. Source is the host path; Name is the folder's own
// name, used verbatim as the mount's target directory name under /data (see
// the mount-building block in Create below). A Name of "" is special: it
// denotes the whole-pool read-only base mount bound at /data itself, which
// must precede every per-folder mount so Docker applies the broader
// read-only view first and the narrower read-write overrides on top of it.
// Not to be confused with the netdisk's external share-link feature
// (分享链接) — kept as "SharedDisk" to avoid any naming collision with
// "Share" elsewhere in this codebase.
type SharedDiskMount struct {
	Source   string
	Name     string
	ReadOnly bool
}

type CreateOptions struct {
	Username    string
	Name        string
	ImageRef    string
	ImageName   string
	Env         []string
	GPUs        string
	Forward8080 bool
	Forward8090 bool
	// RequireLogin8080/RequireLogin8090, when true, stamp the container so its
	// forwarded 8080/8090 port (respectively) is login-gated — independently of
	// each other, so an image can gate one and leave the other open. Server-
	// supplied from the image preset — never taken from the request, so a user
	// cannot self-grant or self-revoke it.
	RequireLogin8080 bool
	RequireLogin8090 bool
	// HTTPS8080/HTTPS8090, when true, additionally open an HTTPS-terminated
	// forward for the 8080/8090 container port: mudp listens on a second host
	// port, terminates TLS there with its self-signed certificate, and relays
	// the decrypted bytes on to the same container port in plaintext. Server-
	// supplied from the image preset, same rationale as RequireLogin8080/8090.
	HTTPS8080 bool
	HTTPS8090 bool
	// NoVNCPasswordEnv8080 names an env var (e.g. "VNC_PW") whose resolved
	// value should be stamped as NoVNCPassword8080Label, so the container list
	// can open the 8080 page pre-authenticated. Server-supplied from the
	// image preset — never taken from the request.
	NoVNCPasswordEnv8080 string
	// NoVNCPasswordParam8080 names the query parameter NoVNCPasswordEnv8080's
	// value is appended as (empty defaults to DefaultNoVNCParam8080,
	// "password"). Server-supplied from the image preset.
	NoVNCPasswordParam8080 string
	// NoVNCPasswordEnv8090 names an env var (e.g. "JUPYTER_TOKEN") whose
	// resolved value should be stamped as NoVNCPassword8090Label, so the
	// container list can open the 8090 page pre-authenticated. Independent of
	// NoVNCPasswordEnv8080, same rationale as RequireLogin8080/8090. Server-
	// supplied from the image preset.
	NoVNCPasswordEnv8090 string
	// NoVNCPasswordParam8090 names the query parameter NoVNCPasswordEnv8090's
	// value is appended as (empty defaults to DefaultNoVNCParam8090, "tkn").
	// Server-supplied from the image preset.
	NoVNCPasswordParam8090 string
	Ports                  []string
	PortPrefix             int
	// Mounts are bind/named-volume mounts: "source:target[:ro]" entries.
	Mounts       []string
	NetdiskPath  string
	MountNetdisk bool
	// MountShm binds the host's /dev/shm into the container so workloads that need
	// large shared memory (ML data loaders, in-memory databases, IPC) are not
	// capped by Docker's default 64MB tmpfs.
	MountShm bool
	// SharedDiskMounts are the shared disk's (共享盘) per-user subfolders to
	// bind in, one mount per folder, when MountSharedDisk is set. Distinct
	// from the netdisk's external share-link feature (分享链接) — "SharedDisk"
	// naming is used throughout to avoid confusion with "Share". Only
	// honored when the image preset opted the image into shared-disk
	// mounting (see store.ImagePreset.MountSharedDisk).
	SharedDiskMounts []SharedDiskMount
	MountSharedDisk  bool
	// Networks are mudp network names to attach the container to.
	Networks []string
	// AllowedNetworks are full Docker names the user may attach to beyond their
	// own mudp-managed networks: the host/shared networks an admin granted to
	// one of their groups. Server-supplied — never taken from the request.
	AllowedNetworks []string
	// ForwardNetworks are the networks an administrator has marked as needing
	// mudp's own port forwarding instead of Docker's publishing (see forward.go).
	// When the container joins one of them, its host ports are recorded on the
	// container and relayed in-process rather than handed to Docker.
	// Server-supplied from the admin settings — never taken from the request.
	ForwardNetworks []string
	// Devices are generic --device specs (host[:container[:rwm]]) to pass through,
	// e.g. /dev/nvidia0. Used by admins to keep NVIDIA GPUs connected to GPU
	// containers and to expose other host devices.
	Devices []string
	// CDIDevices are CDI device names for the Container Device Interface, e.g.
	// nvidia.com/gpu=0. Requires a CDI-aware runtime (dockerd configured for CDI).
	CDIDevices []string
	// RestartPolicy is the Docker restart policy: "no", "always", "unless-stopped",
	// or "on-failure". Defaults to "unless-stopped" when empty.
	RestartPolicy string
	// Advanced create fields (all optional; zero values inherit the image defaults
	// as before, preserving backward compatibility).
	Command    []string          // overrides image CMD
	Entrypoint []string          // overrides image ENTRYPOINT
	WorkingDir string            // overrides image WORKDIR (empty = image default)
	Hostname   string            // container hostname
	RunUser    string            // --user spec, e.g. "root" or "1000:1000"
	NanoCPUs   int64             // CPU quota in nanoseconds (0 = unlimited)
	MemoryMB   int64             // memory limit in MiB (0 = unlimited)
	PidsLimit  int64             // max process count (0 = unlimited; <0 = no limit)
	Sysctls    map[string]string // --sysctl key=value pairs
	CapAdd     []string          // --cap-add capabilities
	CapDrop    []string          // --cap-drop capabilities
	Labels     map[string]string // user-supplied labels (merged on top of mudp.* labels)
	// Progress is an optional callback fired at each creation stage.
	// stage is one of: image, create, start, done.
	Progress func(stage, msg string)
}

// ExecConn wraps a live exec attach used by the WebSocket terminal.
type ExecConn struct {
	Hijacked types.HijackedResponse
	ExecID   string
}

// ExecResult is the completed output of a non-interactive docker exec.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// InspectInfo is the Portainer-style container detail surfaced to the UI.
type InspectInfo struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	ImageName     string            `json:"imageName"`
	State         string            `json:"state"`
	Status        string            `json:"status"`
	CreatedAt     int64             `json:"createdAt"`
	RestartPolicy string            `json:"restartPolicy"`
	Entrypoint    []string          `json:"entrypoint"`
	Cmd           []string          `json:"cmd"`
	Env           []string          `json:"env"`
	Labels        map[string]string `json:"labels"`
	Ports         []PortBinding     `json:"ports"`
	Mounts        []MountInfo       `json:"mounts"`
	Networks      []NetworkInfo     `json:"networks"`
	IPAddress     string            `json:"ipAddress"`
	GPU           string            `json:"gpu"`
	// User is the runtime user the container process runs as (from
	// inspect.Config.User), surfaced for visibility in the details modal.
	User string `json:"user"`
}

type PortBinding struct {
	Host        string `json:"host"`
	HostPort    string `json:"hostPort"`
	PrivatePort uint16 `json:"privatePort"`
	Type        string `json:"type"`
	// Forwarded marks a mapping mudp relays itself rather than one Docker
	// published, so the detail view can say which is which.
	Forwarded bool `json:"forwarded,omitempty"`
}

type MountInfo struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Mode     string `json:"mode"`
	ReadOnly bool   `json:"readOnly"`
}

type NetworkInfo struct {
	Name       string `json:"name"`
	IPAddress  string `json:"ipAddress"`
	Gateway    string `json:"gateway"`
	MacAddress string `json:"macAddress"`
}

func New() (*Client, error) {
	return NewWithHost("")
}

// NewWithHost builds a Docker client. When host is empty the SDK reads
// DOCKER_HOST (or falls back to the platform default socket).
// Close releases the underlying Docker client connections.
func (d *Client) Close() error {
	if d.c == nil {
		return nil
	}
	return d.c.Close()
}

// Raw returns the underlying Docker SDK client. It is an escape hatch for
// callers that need operations not yet wrapped here. Callers must not close it
// — the dockerx.Client owns its lifetime.
func (d *Client) Raw() *client.Client {
	return d.c
}

func NewWithHost(host string) (*Client, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host == "" {
		opts = append(opts, client.FromEnv)
	} else {
		opts = append(opts, client.WithHost(host))
	}
	c, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

func FullContainerName(username, name string) string {
	return Prefix + Slug(username) + "-" + Slug(name)
}

func UserContainerPrefix(username string) string {
	return "/" + Prefix + Slug(username) + "-"
}

func Slug(s string) string {
	s = strings.Trim(cleanPart.ReplaceAllString(strings.ToLower(s), "-"), "-_.")
	if s == "" {
		return "item"
	}
	return s
}

func PublicImageName(name string) string {
	name = strings.TrimPrefix(name, Prefix)
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	return name
}

func MUDPImageRef(display string) string {
	display = strings.TrimPrefix(Slug(display), Prefix)
	return Prefix + display + ":latest"
}

func (d *Client) PullAndTag(ctx context.Context, sourceRef, display string) (string, error) {
	return d.PullAndTagProgress(ctx, sourceRef, display, nil)
}

// PullAndTagProgress pulls an image while invoking progress for each JSON status
// line emitted by the registry, then tags it under the mudp- namespace.
func (d *Client) PullAndTagProgress(ctx context.Context, sourceRef, display string, progress func(line string)) (string, error) {
	if !strings.Contains(sourceRef, ":") {
		sourceRef += ":latest"
	}
	rc, err := d.c.ImagePull(ctx, sourceRef, types.ImagePullOptions{})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Status         string `json:"status"`
			ID             string `json:"id"`
			Progress       string `json:"progress"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			if err != context.Canceled {
				return "", err
			}
			break
		}
		if msg.Error != "" {
			return "", fmt.Errorf("pull error: %s", msg.Error)
		}
		if progress != nil {
			progress(formatPullLine(msg.Status, msg.ID, msg.Progress, msg.ProgressDetail.Current, msg.ProgressDetail.Total))
		}
	}
	target := MUDPImageRef(display)
	if err := d.c.ImageTag(ctx, sourceRef, target); err != nil {
		return "", err
	}
	return target, nil
}

// formatPullLine renders a registry progress message as a compact human line.
func formatPullLine(status, id, progress string, current, total int64) string {
	if id == "" {
		return status
	}
	if progress != "" {
		return fmt.Sprintf("%s: %s (%s)", id, status, progress)
	}
	if total > 0 && current > 0 {
		pct := current * 100 / total
		return fmt.Sprintf("%s: %s %d%%", id, status, pct)
	}
	return fmt.Sprintf("%s: %s", id, status)
}

func (d *Client) RemoveManagedImage(ctx context.Context, ref string) error {
	if !strings.HasPrefix(ref, Prefix) {
		return fmt.Errorf("image %q is not managed by mudp", ref)
	}
	_, err := d.c.ImageRemove(ctx, ref, types.ImageRemoveOptions{Force: true, PruneChildren: true})
	return err
}

func (d *Client) ListManagedImages(ctx context.Context) ([]string, error) {
	items, err := d.c.ImageList(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, img := range items {
		for _, tag := range img.RepoTags {
			if strings.HasPrefix(tag, Prefix) && !strings.Contains(tag, "<none>") && !seen[tag] {
				seen[tag] = true
				out = append(out, tag)
			}
		}
	}
	return out, nil
}

func (d *Client) CreateContainer(ctx context.Context, opts CreateOptions) (string, error) {
	progress := opts.Progress
	if progress == nil {
		progress = func(string, string) {}
	}
	emit := func(stage, msg string) {
		progress(stage, msg)
	}
	name := FullContainerName(opts.Username, opts.Name)
	emit("image", "Inspecting image "+opts.ImageRef)
	imageInfo, _, err := d.c.ImageInspectWithRaw(ctx, opts.ImageRef)
	if err != nil {
		return "", err
	}
	// Networks are resolved before the ports because they decide how a port gets
	// published. A container joining only networks the administrator marked for
	// mudp forwarding takes its host ports from the same range as everyone else,
	// but mudp relays them itself instead of asking Docker to publish them — on
	// such a host (an OpenWrt box owning the firewall) Docker's publishing does
	// not work, while the container's own LAN address does. A container that
	// also joins an ordinary network (bridge, say) skips forwarding and keeps
	// Docker publishing instead, since that plain network can publish the same
	// port normally — forwarding it too would just fight Docker for it. See
	// forward.go.
	allowedNetworks := make(map[string]bool, len(opts.AllowedNetworks))
	for _, n := range opts.AllowedNetworks {
		allowedNetworks[n] = true
	}
	resolvedNetworks := make([]string, 0, len(opts.Networks))
	for _, n := range opts.Networks {
		full, netErr := d.validateNetworkAttachment(ctx, opts.Username, n, allowedNetworks)
		if netErr != nil {
			return "", netErr
		}
		resolvedNetworks = append(resolvedNetworks, full)
	}
	forwardNet := ForwardNetworkFor(resolvedNetworks, opts.ForwardNetworks)

	exposed := nat.PortSet{}
	portMap := nat.PortMap{}
	var forwardSpecs []ForwardSpec
	usedPorts, err := d.managedHostPorts(ctx)
	if err != nil {
		return "", err
	}
	allocated := map[int]bool{}
	nextPort := func() (int, error) {
		if opts.PortPrefix <= 0 {
			return 0, fmt.Errorf("port prefix is not assigned")
		}
		start := opts.PortPrefix * 100
		for p := start; p <= start+99 && p <= 65535; p++ {
			if p < 10000 {
				continue
			}
			if !usedPorts[p] && !allocated[p] && portFree(p) {
				allocated[p] = true
				return p, nil
			}
		}
		return 0, fmt.Errorf("no free ports in assigned range %d-%d", start, start+99)
	}
	// publish records one resolved mapping. The container port is always exposed;
	// where the host side goes depends on the network: Docker's port bindings
	// normally, or the forward label when the container sits on a network mudp
	// forwards for. Nothing else in the create path needs to know which it was.
	publish := func(hostPort, containerPort int, proto string) {
		p := nat.Port(fmt.Sprintf("%d/%s", containerPort, proto))
		exposed[p] = struct{}{}
		if forwardNet != "" {
			forwardSpecs = append(forwardSpecs, ForwardSpec{HostPort: hostPort, ContainerPort: containerPort, Proto: proto})
			return
		}
		portMap[p] = append(portMap[p], nat.PortBinding{HostPort: strconv.Itoa(hostPort)})
	}
	// addPort resolves one mapping from the create form (see parsePortSpec for
	// the accepted shapes) and publishes it. The host side is always allocated
	// from the owner's assigned range.
	addPort := func(spec string) error {
		req, err := parsePortSpec(spec)
		if err != nil {
			return err
		}
		if req.skip {
			return nil
		}
		hostPort, allocErr := nextPort()
		if allocErr != nil {
			return allocErr
		}
		publish(hostPort, req.containerPort, req.proto)
		return nil
	}
	for _, p := range opts.Ports {
		if err := addPort(p); err != nil {
			return "", err
		}
	}
	if opts.Forward8080 {
		hostPort, err := nextPort()
		if err != nil {
			return "", err
		}
		publish(hostPort, 8080, "tcp")
	}
	if opts.Forward8090 {
		hostPort, err := nextPort()
		if err != nil {
			return "", err
		}
		publish(hostPort, 8090, "tcp")
	}
	// HTTPS8080/HTTPS8090 open a second, TLS-terminated listener for the same
	// container port, on its own host port — only possible on a forwarding
	// network, since terminating TLS means mudp itself has to own the socket,
	// which Docker's own port publishing does not give it. Independent of
	// Forward8080/Forward8090: the plain port need not be open for the HTTPS
	// one to be.
	if opts.HTTPS8080 && forwardNet != "" {
		hostPort, err := nextPort()
		if err != nil {
			return "", err
		}
		exposed[nat.Port("8080/tcp")] = struct{}{}
		forwardSpecs = append(forwardSpecs, ForwardSpec{HostPort: hostPort, ContainerPort: 8080, Proto: "tcp", TLS: true})
	}
	if opts.HTTPS8090 && forwardNet != "" {
		hostPort, err := nextPort()
		if err != nil {
			return "", err
		}
		exposed[nat.Port("8090/tcp")] = struct{}{}
		forwardSpecs = append(forwardSpecs, ForwardSpec{HostPort: hostPort, ContainerPort: 8090, Proto: "tcp", TLS: true})
	}
	labels := map[string]string{
		ManagedLabel:       "true",
		UserLabel:          opts.Username,
		NameLabel:          opts.Name,
		"mudp.image":       opts.ImageName,
		"mudp.gpu":         opts.GPUs,
		"mudp.forward8080": fmt.Sprint(opts.Forward8080),
		"mudp.forward8090": fmt.Sprint(opts.Forward8090),
	}
	// Merge user-supplied labels on top of the managed labels, but never let a
	// caller overwrite the mudp.* keys (ownership/management must stay intact).
	labels = mergeUserLabels(labels, opts.Labels)
	// The forward labels are written after the merge (which refuses mudp.* keys
	// from a caller) so the mapping mudp will relay cannot be forged from the
	// request: it is exactly what the port allocator just handed out.
	if forwardNet != "" && len(forwardSpecs) > 0 {
		labels[ForwardPortsLabel] = FormatForwardSpecs(forwardSpecs)
		labels[ForwardNetLabel] = forwardNet
		// Login-gating is stamped only when the container actually forwards, so
		// a gated image run on a non-forwarding network does not carry a stale
		// label that would do nothing but confuse an admin reading the labels.
		// Per-port: an image can gate 8080 without gating 8090, or vice versa.
		var loginPorts []string
		if opts.RequireLogin8080 {
			loginPorts = append(loginPorts, "8080")
		}
		if opts.RequireLogin8090 {
			loginPorts = append(loginPorts, "8090")
		}
		if len(loginPorts) > 0 {
			labels[ForwardLoginPortsLabel] = strings.Join(loginPorts, ",")
		}
	}
	// Stamp the resolved 8080/8090 auto-login secrets (if the image preset
	// named env vars for them), plus the query parameter each is appended as,
	// so the container list can build the open links without a second inspect
	// call per row. Server-supplied, same rationale as RequireLogin.
	// Independent per port, so a secret meant for one port's page is not
	// tacked onto an unrelated service on the other.
	if key := strings.TrimSpace(opts.NoVNCPasswordEnv8080); key != "" {
		if val, ok := lookupEnvValue(opts.Env, key); ok && val != "" {
			labels[NoVNCPassword8080Label] = val
			param := strings.TrimSpace(opts.NoVNCPasswordParam8080)
			if param == "" {
				param = DefaultNoVNCParam8080
			}
			labels[NoVNCParam8080Label] = param
		}
	}
	if key := strings.TrimSpace(opts.NoVNCPasswordEnv8090); key != "" {
		if val, ok := lookupEnvValue(opts.Env, key); ok && val != "" {
			labels[NoVNCPassword8090Label] = val
			param := strings.TrimSpace(opts.NoVNCPasswordParam8090)
			if param == "" {
				param = DefaultNoVNCParam8090
			}
			labels[NoVNCParam8090Label] = param
		}
	}
	hostCfg := &container.HostConfig{
		PortBindings:  portMap,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(normalizeRestartPolicy(opts.RestartPolicy))},
		CapAdd:        opts.CapAdd,
		CapDrop:       opts.CapDrop,
		Sysctls:       opts.Sysctls,
	}
	// Resource limits. NanoCPUs/Memory are only set when positive so a zero
	// value keeps the image/host default (no cgroup limit).
	if opts.NanoCPUs > 0 {
		hostCfg.Resources.NanoCPUs = opts.NanoCPUs
	}
	if opts.MemoryMB > 0 {
		hostCfg.Resources.Memory = opts.MemoryMB * 1024 * 1024
	}
	// PidsLimit semantics: 0 = unlimited here (we treat 0 as "unset"), and a
	// negative input is clamped to the SDK's "no limit" sentinel (-1). Docker
	// uses int64 with a pointer in newer versions; the struct field accepts 0
	// for default and -1 for unlimited.
	if opts.PidsLimit != 0 {
		limit := opts.PidsLimit
		if limit < 0 {
			limit = 0
		}
		hostCfg.Resources.PidsLimit = &limit
	}
	for _, m := range opts.Mounts {
		mount, err := parseMount(m)
		if err != nil {
			return "", err
		}
		if err := d.validateMountSource(ctx, opts.Username, &mount); err != nil {
			return "", err
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mount)
	}
	if opts.MountNetdisk && strings.TrimSpace(opts.NetdiskPath) != "" {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{Type: mount.TypeBind, Source: strings.TrimSpace(opts.NetdiskPath), Target: "/workspace"})
	}
	if opts.MountShm {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{Type: mount.TypeBind, Source: "/dev/shm", Target: "/dev/shm"})
	}
	if opts.MountSharedDisk {
		// Two-layer structure (see sharedDiskMountsFor): a read-only bind of
		// the whole pool at /data so new member subfolders appear in
		// already-running containers, then one read-write bind per folder
		// that must be writable, overriding the base:
		//   -v /pool:/data:ro
		//   -v /pool/userme:/data/userme:rw
		// Read-only folders need no mount of their own — the base covers
		// them. The base mount (Name == "") must come first; Docker applies
		// the broader read-only view and the narrower per-folder mounts
		// override it. Order is guaranteed by the caller.
		for _, m := range opts.SharedDiskMounts {
			source := strings.TrimSpace(m.Source)
			if source == "" {
				continue
			}
			target := "/data/" + m.Name
			if m.Name == "" {
				target = "/data"
			}
			hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
				Type: mount.TypeBind, Source: source, Target: target, ReadOnly: m.ReadOnly,
			})
		}
	}
	if opts.GPUs != "" && opts.GPUs != "none" {
		hostCfg.Resources.DeviceRequests = []container.DeviceRequest{{Driver: "nvidia", Count: -1, Capabilities: [][]string{{"gpu"}}}}
	}
	for _, spec := range opts.Devices {
		dm, err := parseDevice(spec)
		if err != nil {
			return "", err
		}
		hostCfg.Devices = append(hostCfg.Devices, dm)
	}
	if len(opts.CDIDevices) > 0 {
		hostCfg.Resources.DeviceRequests = append(hostCfg.Resources.DeviceRequests, container.DeviceRequest{Driver: "cdi", DeviceIDs: opts.CDIDevices})
	}
	env := append([]string{}, opts.Env...)
	if opts.GPUs != "" && opts.GPUs != "none" {
		env = append(env, "NVIDIA_VISIBLE_DEVICES="+opts.GPUs)
	}
	containerCfg := &container.Config{
		Image:        opts.ImageRef,
		Env:          env,
		Tty:          true,
		OpenStdin:    true,
		ExposedPorts: exposed,
		Labels:       labels,
		WorkingDir:   imageInfo.Config.WorkingDir,
	}
	// Advanced overrides: only apply when the caller supplied a value, so the
	// image defaults are preserved when the fields are empty (backward compat).
	if len(opts.Command) > 0 {
		containerCfg.Cmd = opts.Command
	}
	if len(opts.Entrypoint) > 0 {
		containerCfg.Entrypoint = opts.Entrypoint
	}
	if strings.TrimSpace(opts.WorkingDir) != "" {
		containerCfg.WorkingDir = opts.WorkingDir
	}
	if strings.TrimSpace(opts.Hostname) != "" {
		containerCfg.Hostname = opts.Hostname
	}
	if strings.TrimSpace(opts.RunUser) != "" {
		containerCfg.User = opts.RunUser
	}
	emit("create", "Creating container")
	resp, err := d.c.ContainerCreate(ctx, containerCfg, hostCfg, networkingConfig(resolvedNetworks), &v1.Platform{}, name)
	if err != nil {
		return "", err
	}
	emit("start", "Starting container")
	if err := d.c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = d.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", err
	}
	emit("done", "Container created")
	return resp.ID, nil
}

func (d *Client) managedHostPorts(ctx context.Context) (map[int]bool, error) {
	args := filters.NewArgs(filters.Arg("label", ManagedLabel+"=true"))
	list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, c := range list {
		for _, p := range c.Ports {
			if p.PublicPort > 0 {
				out[int(p.PublicPort)] = true
			}
		}
		// Forwarded ports are not published, so Docker does not report them —
		// but they are just as much allocated, and a stopped container keeps its
		// claim so restarting it does not find its port taken by a newer one.
		for _, s := range ParseForwardSpecs(c.Labels[ForwardPortsLabel]) {
			out[s.HostPort] = true
		}
	}
	return out, nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

type TopProcess struct {
	ContainerID   string  `json:"containerId"`
	Container     string  `json:"container"`
	User          string  `json:"user"`
	PID           string  `json:"pid"`
	CPUPercent    float64 `json:"cpuPct"`
	MemoryMB      float64 `json:"memMb"`
	MemoryPercent float64 `json:"memPct"`
	Command       string  `json:"command"`
}

type GPUUsage struct {
	Percent       float64 `json:"gpuPct"`
	MemoryMB      float64 `json:"gpuMemMb"`
	MemoryTotalMB float64 `json:"gpuMemTotalMb"`
	MemoryPct     float64 `json:"gpuMemPct"`
}

// GPUCard is a per-GPU snapshot for the hardware monitoring page. It carries the
// richer set of fields (name, temperature, power) that the dashboard/usage rollups
// don't need. All numeric fields are best-effort and default to zero when nvidia-smi
// couldn't report them (e.g. no power sensors on consumer cards).
type GPUCard struct {
	Index      int     `json:"index"`
	Name       string  `json:"name"`
	UtilPct    float64 `json:"utilPct"`
	MemUsedMB  float64 `json:"memUsedMb"`
	MemTotalMB float64 `json:"memTotalMb"`
	MemPct     float64 `json:"memPct"`
	TempC      float64 `json:"tempC"`
	PowerW     float64 `json:"powerW"`
	MemUtilPct float64 `json:"memUtilPct"`
}

type gpuMetric struct {
	Index       string
	Name        string
	Percent     float64
	MemoryMB    float64
	MemoryTotal float64
	TempC       float64
	PowerW      float64
	MemUtilPct  float64
}

func (d *Client) GPUUsage(ctx context.Context, spec string) (GPUUsage, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "none" {
		return GPUUsage{}, nil
	}
	// Skip the subprocess entirely when nvidia-smi is unavailable; this keeps
	// ListContainers fast and quiet on non-GPU hosts.
	if !nvidiaSmiAvailable() {
		return GPUUsage{}, nil
	}
	metrics := queryGPU(ctx)
	selected := selectGPUMetrics(metrics, spec)
	if len(selected) == 0 {
		return GPUUsage{}, nil
	}
	var usage GPUUsage
	for _, m := range selected {
		usage.Percent += m.Percent
		usage.MemoryMB += m.MemoryMB
		usage.MemoryTotalMB += m.MemoryTotal
	}
	usage.Percent = round2(usage.Percent / float64(len(selected)))
	if usage.MemoryTotalMB > 0 {
		usage.MemoryPct = round2(usage.MemoryMB / usage.MemoryTotalMB * 100)
	}
	usage.MemoryMB = round2(usage.MemoryMB)
	usage.MemoryTotalMB = round2(usage.MemoryTotalMB)
	return usage, nil
}

// parseGPUMetrics parses the CSV output of the extended nvidia-smi query. Field
// order matches the query in gpu.go:
//
//	index, name, utilization.gpu, memory.used, memory.total,
//	temperature.gpu, power.draw, utilization.memory
//
// Any field that nvidia-smi could not report (e.g. "[N/A]") parses to 0.
func parseGPUMetrics(raw string) []gpuMetric {
	var out []gpuMetric
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 5 {
			continue
		}
		// index,name,util.gpu,mem.used,mem.total are guaranteed by the query; the
		// remaining fields (temp, power, mem util) may be absent on some GPUs.
		idx := strings.TrimSpace(parts[0])
		name := strings.TrimSpace(parts[1])
		pct, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		mem, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
		total, _ := strconv.ParseFloat(strings.TrimSpace(parts[4]), 64)
		var temp, power, memUtil float64
		if len(parts) > 5 {
			temp, _ = strconv.ParseFloat(strings.TrimSpace(parts[5]), 64)
		}
		if len(parts) > 6 {
			power, _ = strconv.ParseFloat(strings.TrimSpace(parts[6]), 64)
		}
		if len(parts) > 7 {
			memUtil, _ = strconv.ParseFloat(strings.TrimSpace(parts[7]), 64)
		}
		out = append(out, gpuMetric{
			Index:       idx,
			Name:        name,
			Percent:     pct,
			MemoryMB:    mem,
			MemoryTotal: total,
			TempC:       temp,
			PowerW:      power,
			MemUtilPct:  memUtil,
		})
	}
	return out
}

// GPUList returns a per-GPU snapshot suitable for the hardware monitoring page.
// It returns an empty slice when nvidia-smi is unavailable (e.g. non-GPU host).
func (d *Client) GPUList(ctx context.Context) []GPUCard {
	if !nvidiaSmiAvailable() {
		return nil
	}
	metrics := queryGPU(ctx)
	cards := make([]GPUCard, 0, len(metrics))
	for _, m := range metrics {
		idx, _ := strconv.Atoi(strings.TrimSpace(m.Index))
		card := GPUCard{
			Index:      idx,
			Name:       m.Name,
			UtilPct:    round2(m.Percent),
			MemUsedMB:  round2(m.MemoryMB),
			MemTotalMB: round2(m.MemoryTotal),
			TempC:      round2(m.TempC),
			PowerW:     round2(m.PowerW),
			MemUtilPct: round2(m.MemUtilPct),
		}
		if card.MemTotalMB > 0 {
			card.MemPct = round2(card.MemUsedMB / card.MemTotalMB * 100)
		}
		cards = append(cards, card)
	}
	return cards
}

func selectGPUMetrics(metrics []gpuMetric, spec string) []gpuMetric {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if spec == "" || spec == "none" {
		return nil
	}
	if spec == "all" || spec == "true" {
		return metrics
	}
	wanted := map[string]bool{}
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	}) {
		part = strings.TrimSpace(strings.TrimPrefix(part, "gpu="))
		if part != "" {
			wanted[part] = true
		}
	}
	var out []gpuMetric
	for _, m := range metrics {
		if wanted[strings.ToLower(m.Index)] {
			out = append(out, m)
		}
	}
	return out
}

func (d *Client) TopProcesses(ctx context.Context, containers []Container) []TopProcess {
	var out []TopProcess
	for _, c := range containers {
		procs, err := d.ContainerTop(ctx, c)
		if err != nil {
			continue
		}
		out = append(out, procs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUPercent > out[j].CPUPercent })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// ContainerTop lists the processes running inside one container, parsed the
// same way TopProcesses does (`docker top <id> aux`). Unlike TopProcesses it
// keeps every row, unsorted, and reports errors so callers (the process
// watcher) can distinguish "container stopped" from "no processes".
func (d *Client) ContainerTop(ctx context.Context, c Container) ([]TopProcess, error) {
	top, err := d.c.ContainerTop(ctx, c.ID, []string{"aux"})
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, t := range top.Titles {
		idx[strings.ToUpper(t)] = i
	}
	out := make([]TopProcess, 0, len(top.Processes))
	for _, p := range top.Processes {
		get := func(keys ...string) string {
			for _, k := range keys {
				if i, ok := idx[k]; ok && i < len(p) {
					return p[i]
				}
			}
			return ""
		}
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(get("%CPU", "CPU"), "%"), 64)
		memPct, _ := strconv.ParseFloat(strings.TrimSuffix(get("%MEM", "MEM"), "%"), 64)
		// RSS is reported in kilobytes by `docker top <id> aux`.
		rssKb, _ := strconv.ParseFloat(get("RSS"), 64)
		out = append(out, TopProcess{
			ContainerID:   c.ID,
			Container:     c.Name,
			User:          c.Labels[UserLabel],
			PID:           get("PID"),
			CPUPercent:    cpu,
			MemoryMB:      round2(rssKb / 1024),
			MemoryPercent: memPct,
			Command:       get("COMMAND", "CMD"),
		})
	}
	return out, nil
}

func (d *Client) ListContainers(ctx context.Context, username string, admin bool, forwardNetworks []string) ([]Container, error) {
	return d.listContainers(ctx, username, admin, false, forwardNetworks)
}

func (d *Client) ListContainersWithSize(ctx context.Context, username string, admin bool, forwardNetworks []string) ([]Container, error) {
	return d.listContainers(ctx, username, admin, true, forwardNetworks)
}

func (d *Client) listContainers(ctx context.Context, username string, admin, includeSize bool, forwardNetworks []string) ([]Container, error) {
	args := filters.NewArgs(filters.Arg("label", "mudp.managed=true"))
	list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Size: includeSize, Filters: args})
	if err != nil {
		return nil, err
	}
	var out []Container
	for _, c := range list {
		full := ""
		if len(c.Names) > 0 {
			full = strings.TrimPrefix(c.Names[0], "/")
		}
		if !strings.HasPrefix(full, Prefix) {
			continue
		}
		if !admin && !strings.HasPrefix("/"+full, UserContainerPrefix(username)) {
			continue
		}
		display := c.Labels["mudp.name"]
		if display == "" && !admin {
			display = strings.TrimPrefix("/"+full, UserContainerPrefix(username))
		}
		// Forwarded ports are mudp's own, so Docker reports them as merely
		// exposed. Resolve them the same way ForwardRules does — from the label,
		// or adopted from a container's current bindings when it reached a
		// forwarding network some other way (e.g. switched onto it after create
		// via network settings) — so the list agrees with what mudp is actually
		// relaying instead of showing a stale bridge-style binding, or none at
		// all. Render them first and skip the bare "8080/tcp" duplicate Docker
		// reports for the same port, or the list would claim a mapped port is
		// unmapped.
		_, forwards := EffectiveForwardSpecs(c, forwardNetworks)
		forwarded := len(forwards) > 0
		ports := make([]string, 0, len(c.Ports)+len(forwards))
		seenPorts := map[string]bool{}
		for _, s := range forwards {
			rendered := s.String()
			if !seenPorts[rendered] {
				seenPorts[rendered] = true
				ports = append(ports, rendered)
			}
		}
		for _, p := range c.Ports {
			var rendered string
			if p.PublicPort > 0 {
				rendered = fmt.Sprintf("%d:%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			} else if forwardHostPort(forwards, int(p.PrivatePort), p.Type) > 0 {
				continue
			} else {
				rendered = fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			}
			if !seenPorts[rendered] {
				seenPorts[rendered] = true
				ports = append(ports, rendered)
			}
		}
		// Each port auto-logs in via its own resolved secret + query parameter
		// name (default "password" for 8080, "tkn" for 8090; admin-overridable
		// per image preset). Independent per port.
		password8080 := c.Labels[NoVNCPassword8080Label]
		param8080 := c.Labels[NoVNCParam8080Label]
		if param8080 == "" {
			param8080 = DefaultNoVNCParam8080
		}
		tkn8090 := c.Labels[NoVNCPassword8090Label]
		param8090 := c.Labels[NoVNCParam8090Label]
		if param8090 == "" {
			param8090 = DefaultNoVNCParam8090
		}
		links := buildPortLinks(c.Ports, forwards)
		for i := range links {
			// The 8080/8090 presets auto-login via their own resolved secret;
			// every other port opens plain.
			switch links[i].Port {
			case 8080:
				links[i].URL = withAutoLoginQuery(links[i].URL, param8080, password8080)
			case 8090:
				links[i].URL = withAutoLoginQuery(links[i].URL, param8090, tkn8090)
			}
		}
		out = append(out, Container{
			ID: c.ID, Name: display, FullName: full, Owner: c.Labels[UserLabel], Image: c.Labels["mudp.image"], State: c.State, Status: c.Status,
			Ports: ports, Labels: c.Labels, DiskMB: float64(c.SizeRw) / 1024 / 1024, GPU: c.Labels["mudp.gpu"], CreatedAt: c.Created,
			PortLinks: links, Forwarded: forwarded,
		})
	}
	for i := range out {
		if out[i].State == "running" {
			mem, _ := d.memoryMB(ctx, out[i].ID)
			out[i].MemoryMB = mem
			if gpu, _ := d.GPUUsage(ctx, out[i].GPU); gpu.MemoryTotalMB > 0 || gpu.Percent > 0 {
				out[i].GPUPercent = gpu.Percent
				out[i].GPUMemoryMB = gpu.MemoryMB
				out[i].GPUMemoryTotalMB = gpu.MemoryTotalMB
				out[i].GPUMemoryPct = gpu.MemoryPct
			}
		}
	}
	return out, nil
}

// lookupEnvValue returns the value of key within a "KEY=VALUE" env list.
func lookupEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if val, ok := strings.CutPrefix(e, prefix); ok {
			return val, true
		}
	}
	return "", false
}

// withAutoLoginQuery appends a "?<param>=<value>" query string to a
// forwarded-port open URL when the container carries a resolved auto-login
// value, so clicking it logs straight into the page (noVNC's "?password=",
// or a token-style "?tkn=") instead of stopping at its login prompt.
func withAutoLoginQuery(rawURL, param, value string) string {
	if rawURL == "" || value == "" {
		return rawURL
	}
	return rawURL + "/?" + param + "=" + url.QueryEscape(value)
}

// buildPortLinks resolves one clickable access URL per reachable TCP container
// port: the https:// one when the port has a TLS-terminated forward (in which
// case its plain http:// link is deliberately omitted — the whole point of the
// HTTPS preset is that this port is served securely), otherwise the http://
// one for a port published by Docker or relayed by mudp. A forwarded port
// answers on the host exactly like a published one, so both resolve the same
// way. Ports with neither a public binding nor a forward are not reachable
// from the host and get no link.
func buildPortLinks(ports []types.Port, forwards []ForwardSpec) []PortLink {
	type candidate struct{ http, https string }
	m := map[int]*candidate{}
	get := func(p int) *candidate {
		if e := m[p]; e != nil {
			return e
		}
		e := &candidate{}
		m[p] = e
		return e
	}
	for _, s := range forwards {
		if s.Proto != "tcp" {
			continue
		}
		e := get(s.ContainerPort)
		if s.TLS {
			if e.https == "" {
				e.https = fmt.Sprintf("https://127.0.0.1:%d", s.HostPort)
			}
		} else if e.http == "" {
			e.http = fmt.Sprintf("http://127.0.0.1:%d", s.HostPort)
		}
	}
	for _, p := range ports {
		if p.PublicPort == 0 || p.Type != "tcp" {
			continue
		}
		e := get(int(p.PrivatePort))
		if e.http != "" {
			continue
		}
		host := p.IP
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		e.http = fmt.Sprintf("http://%s:%d", host, p.PublicPort)
	}
	order := make([]int, 0, len(m))
	for p := range m {
		order = append(order, p)
	}
	sort.Ints(order)
	out := make([]PortLink, 0, len(order))
	for _, p := range order {
		e := m[p]
		if e.https != "" {
			out = append(out, PortLink{Port: p, URL: e.https, TLS: true})
		} else if e.http != "" {
			out = append(out, PortLink{Port: p, URL: e.http})
		}
	}
	return out
}

func (d *Client) memoryMB(ctx context.Context, id string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	resp, err := d.c.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var s struct {
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Stats memoryStatsDetail `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return 0, err
	}
	// Report the working set (usage minus reclaimable file cache) so the listing
	// matches the value shown in the live stats stream. See workingSet for the
	// rationale behind the subtrahend choice across cgroup v1/v2.
	usage := workingSet(s.MemoryStats.Usage, s.MemoryStats.Stats)
	return float64(usage) / 1024 / 1024, nil
}

func (d *Client) Action(ctx context.Context, id, action string) error {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimPrefix(inspect.Name, "/"), Prefix) || inspect.Config.Labels["mudp.managed"] != "true" {
		return fmt.Errorf("container is not managed by mudp")
	}
	switch action {
	case "start":
		return d.c.ContainerStart(ctx, id, container.StartOptions{})
	case "stop":
		timeout := 10
		return d.c.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	case "restart":
		return d.Restart(ctx, id)
	case "remove":
		return d.c.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: true})
	case "pause":
		return d.c.ContainerPause(ctx, id)
	case "unpause":
		return d.c.ContainerUnpause(ctx, id)
	case "kill":
		// SIGKILL forces immediate termination, unlike "stop" (SIGTERM + grace).
		return d.c.ContainerKill(ctx, id, "SIGKILL")
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

// Restart stops then starts a managed container.
func (d *Client) Restart(ctx context.Context, id string) error {
	if err := d.managedGuard(ctx, id); err != nil {
		return err
	}
	timeout := 10
	if err := d.c.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		return err
	}
	return d.c.ContainerStart(ctx, id, container.StartOptions{})
}

// UpdateContainerSettings applies post-create edits to a managed container:
// the restart policy and the set of attached networks. Both are optional — pass
// nil to leave a field unchanged. Network changes take effect immediately; a
// restart policy change applies on the next start (Docker semantics), so callers
// that want it effective immediately should restart the container themselves.
func (d *Client) UpdateContainerSettings(ctx context.Context, id string, restartPolicy *string, networks []string) error {
	if err := d.managedGuard(ctx, id); err != nil {
		return err
	}
	if restartPolicy != nil {
		policy := normalizeRestartPolicy(*restartPolicy)
		if _, err := d.c.ContainerUpdate(ctx, id, container.UpdateConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(policy)},
		}); err != nil {
			return fmt.Errorf("update restart policy: %w", err)
		}
	}
	if networks != nil {
		inspect, err := d.c.ContainerInspect(ctx, id)
		if err != nil {
			return fmt.Errorf("inspect before network change: %w", err)
		}
		// Disconnect every currently-attached network, then (re)connect the
		// requested set. The Docker API does not support a wholesale replace.
		for name := range inspect.NetworkSettings.Networks {
			_ = d.c.NetworkDisconnect(ctx, name, id, true)
		}
		for _, name := range networks {
			n := strings.TrimSpace(name)
			if n == "" {
				continue
			}
			if err := d.c.NetworkConnect(ctx, n, id, nil); err != nil {
				// Don't fail the whole update on one bad network; the rest still apply.
				continue
			}
		}
	}
	return nil
}

// normalizeRestartPolicy returns a valid Docker restart policy name, defaulting
// to "unless-stopped" when the input is empty or unrecognised.
func normalizeRestartPolicy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "no", "always", "unless-stopped", "on-failure":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		// Empty or unrecognised input defaults to "unless-stopped".
		return "unless-stopped"
	}
}

// managedGuard rejects containers that are not owned by mudp.
func (d *Client) managedGuard(ctx context.Context, id string) error {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimPrefix(inspect.Name, "/"), Prefix) || inspect.Config.Labels["mudp.managed"] != "true" {
		return fmt.Errorf("container is not managed by mudp")
	}
	return nil
}

// Inspect returns a Portainer-style detail snapshot.
func (d *Client) Inspect(ctx context.Context, id string) (InspectInfo, error) {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return InspectInfo{}, err
	}
	info := InspectInfo{
		ID:            inspect.ID,
		Name:          strings.TrimPrefix(inspect.Name, "/"),
		Image:         inspect.Image,
		State:         inspect.State.Status,
		Status:        inspect.State.Status,
		RestartPolicy: string(inspect.HostConfig.RestartPolicy.Name),
		Entrypoint:    inspect.Config.Entrypoint,
		Cmd:           inspect.Config.Cmd,
		Env:           inspect.Config.Env,
		Labels:        inspect.Config.Labels,
		GPU:           inspect.Config.Labels["mudp.gpu"],
		ImageName:     inspect.Config.Labels["mudp.image"],
		User:          inspect.Config.User,
	}
	if info.Labels == nil {
		info.Labels = map[string]string{}
	}
	if t, err := time.Parse(time.RFC3339Nano, inspect.Created); err == nil {
		info.CreatedAt = t.Unix()
	}
	// mudp-forwarded ports carry no Docker binding, so an unbound port is
	// reported with the host port the label assigns it. Anything the label
	// forwards that the image never exposed is added afterwards, so the detail
	// view lists every mapping that actually answers on the host.
	forwards := ParseForwardSpecs(info.Labels[ForwardPortsLabel])
	// forwardShown tracks which forwarded container ports have been rendered
	// already; seenMapping collapses the duplicates Docker reports for one
	// published port (the IPv4 and IPv6 bindings, "0.0.0.0:10503" and
	// "[::]:10503"), which otherwise show up as the same mapping listed twice.
	forwardShown := map[string]bool{}
	seenMapping := map[string]bool{}
	for port, bindings := range inspect.NetworkSettings.Ports {
		portType := port.Proto()
		privatePort := port.Int()
		if len(bindings) == 0 {
			if hp := forwardHostPort(forwards, privatePort, portType); hp > 0 {
				forwardShown[fmt.Sprintf("%d/%s", privatePort, portType)] = true
				info.Ports = append(info.Ports, PortBinding{HostPort: strconv.Itoa(hp), PrivatePort: uint16(privatePort), Type: portType, Forwarded: true})
				continue
			}
			info.Ports = append(info.Ports, PortBinding{PrivatePort: uint16(privatePort), Type: portType})
			continue
		}
		for _, b := range bindings {
			key := fmt.Sprintf("%s:%d/%s", b.HostPort, privatePort, portType)
			if seenMapping[key] {
				continue
			}
			seenMapping[key] = true
			info.Ports = append(info.Ports, PortBinding{Host: b.HostIP, HostPort: b.HostPort, PrivatePort: uint16(privatePort), Type: portType})
		}
	}
	for _, s := range forwards {
		if forwardShown[fmt.Sprintf("%d/%s", s.ContainerPort, s.Proto)] {
			continue
		}
		info.Ports = append(info.Ports, PortBinding{HostPort: strconv.Itoa(s.HostPort), PrivatePort: uint16(s.ContainerPort), Type: s.Proto, Forwarded: true})
	}
	for _, m := range inspect.Mounts {
		// m.Mode is the raw options string the user supplied at mount time (e.g.
		// selinux relabeling flags) and is not reliably "ro"/"rw" — for named
		// volumes it is often empty. m.RW is Docker's own resolved read-write
		// bit, so it's what the UI's ro/rw badge is built from.
		info.Mounts = append(info.Mounts, MountInfo{Type: string(m.Type), Source: m.Source, Target: m.Destination, Mode: m.Mode, ReadOnly: !m.RW})
	}
	for name, net := range inspect.NetworkSettings.Networks {
		info.Networks = append(info.Networks, NetworkInfo{Name: name, IPAddress: net.IPAddress, Gateway: net.Gateway, MacAddress: net.MacAddress})
		if info.IPAddress == "" && net.IPAddress != "" {
			info.IPAddress = net.IPAddress
		}
	}
	return info, nil
}

// InspectRaw returns Docker's full inspect document as raw JSON. It is the
// backend for the "native JSON inspect" view. Ownership is enforced by the
// caller (server containerOwnedBy); this only re-checks the managed guard so
// raw inspect never escapes non-mudp containers.
func (d *Client) InspectRaw(ctx context.Context, id string) (json.RawMessage, error) {
	if err := d.managedGuard(ctx, id); err != nil {
		return nil, err
	}
	_, raw, err := d.c.ContainerInspectWithRaw(ctx, id, true)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// DuplicateContainer creates a new container that mirrors an existing one's
// configuration (image, env, ports, mounts, networks, GPU, restart policy),
// without starting it. The original container is left untouched. The new
// container inherits the same owner (username) so the ownership prefix is
// preserved. portPrefix is required to re-publish host ports; pass the owner's
// assigned prefix. allowedNetworks is the caller's attachable set (see
// CreateOptions.AllowedNetworks): the copy is created through the same
// validation as a fresh container, so a network the owner may no longer join —
// a "bridge" since restricted to other groups, say — fails the duplicate rather
// than being reproduced. forwardNetworks carries the administrator's current
// forwarding setting, so the copy publishes its ports the way the host works
// today rather than the way it worked when the original was created.
func (d *Client) DuplicateContainer(ctx context.Context, id, newName, username string, portPrefix int, allowedNetworks, forwardNetworks []string) (string, error) {
	if err := d.managedGuard(ctx, id); err != nil {
		return "", err
	}
	info, err := d.Inspect(ctx, id)
	if err != nil {
		return "", err
	}
	// Translate the ports back into the create spec. Public ports are host-side
	// numbers that won't be reused verbatim — express them as ":container" so
	// CreateContainer auto-allocates a fresh host port from the owner's range.
	// The protocol travels with them: a copy of a container forwarding 53/udp
	// must not come back as 53/tcp.
	var ports []string
	seenPort := map[string]bool{}
	for _, p := range info.Ports {
		if p.PrivatePort == 0 {
			continue
		}
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		spec := fmt.Sprintf(":%d/%s", p.PrivatePort, proto)
		if seenPort[spec] {
			continue
		}
		seenPort[spec] = true
		ports = append(ports, spec)
	}
	// Translate named-volume mounts (bind mounts and netdisk are excluded to
	// avoid double-mounting the host netdisk path or host bind sources, which
	// the create flow manages separately).
	var mounts []string
	for _, m := range info.Mounts {
		if m.Type == "volume" && strings.HasPrefix(m.Source, Prefix) {
			// Strip the mudp-<user>-vol- prefix to recover the display name.
			display := dequalifyVolumeName(m.Source, username)
			if display != "" {
				mounts = append(mounts, display+":"+m.Target)
			}
		}
	}
	// Translate networks back to display names where possible (strip the
	// mudp-<user>-net- prefix). Built-in names are passed through as-is and
	// validated against allowedNetworks like everything else.
	var networks []string
	for _, n := range info.Networks {
		if n.Name == "" {
			continue
		}
		if IsSystemNetworkName(n.Name) {
			networks = append(networks, n.Name)
			continue
		}
		display := dequalifyNetworkName(n.Name, username)
		if display != "" {
			networks = append(networks, display)
		}
	}
	opts := CreateOptions{
		Username:        username,
		Name:            newName,
		ImageRef:        info.Image,
		ImageName:       info.ImageName,
		Env:             info.Env,
		GPUs:            info.GPU,
		Ports:           ports,
		PortPrefix:      portPrefix,
		Mounts:          mounts,
		Networks:        networks,
		AllowedNetworks: allowedNetworks,
		ForwardNetworks: forwardNetworks,
		RestartPolicy:   info.RestartPolicy,
	}
	// Duplicate does not auto-start; CreateContainer always starts, so create
	// the container then stop it immediately to match Portainer's behavior.
	createdID, err := d.CreateContainer(ctx, opts)
	if err != nil {
		return "", err
	}
	timeout := 5
	_ = d.c.ContainerStop(ctx, createdID, container.StopOptions{Timeout: &timeout})
	return createdID, nil
}

// hijacked connection used by the WebSocket terminal pump. It prefers bash
// (for full readline-style Tab completion and history) and falls back to sh.
func (d *Client) ExecAttach(ctx context.Context, id string, rows, cols uint) (ExecConn, error) {
	if err := d.managedGuard(ctx, id); err != nil {
		return ExecConn{}, err
	}
	shell := pickShell(ctx, d.c, id)
	// A coloured prompt + xterm-256color TERM so ls/gcc/etc. emit ANSI colours.
	// The prompt is: green user@host, blue cwd, grey $?  prompt marker.
	colourPrompt := "\\[\\e[1;32m\\]\\u@\\h\\[\\e[0m\\]:\\[\\e[1;34m\\]\\w\\[\\e[0m\\]\\$ "
	execCfg := types.ExecConfig{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          []string{shell},
		Env: []string{
			"TERM=xterm-256color",
			"COLORTERM=truecolor",
			"PS1=" + colourPrompt,
			"PS2=" + colourPrompt,
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
			// Force colour on for GNU tools that check these flags.
			"LS_COLORS=rs=0:di=01;34:ln=01;36:mh=00:pi=40;33:so=01;35:do=01;35:bd=40;33;01:cd=40;33;01:or=40;31;01:mi=00:su=37;41:sg=30;43:ca=00:tw=30;42:ow=34;42:st=37;44:ex=01;32",
			"GCC_COLORS=error=01;31:warning=01;35:note=01;36:caret=01;32:locus=01:quote=01",
			"CLICOLOR=1",
			"CLICOLOR_FORCE=1",
			"FORCE_COLOR=1",
		},
	}
	if cols > 0 || rows > 0 {
		execCfg.ConsoleSize = &[2]uint{rows, cols}
	}
	createResp, err := d.c.ContainerExecCreate(ctx, id, execCfg)
	if err != nil {
		return ExecConn{}, err
	}
	attachResp, err := d.c.ContainerExecAttach(ctx, createResp.ID, types.ExecStartCheck{Tty: true, ConsoleSize: &[2]uint{rows, cols}})
	if err != nil {
		return ExecConn{}, err
	}
	return ExecConn{Hijacked: attachResp, ExecID: createResp.ID}, nil
}

// Exec runs a non-interactive command in a managed, running container and waits
// for it to finish.
func (d *Client) Exec(ctx context.Context, id string, cmd []string, user string, env []string) (ExecResult, error) {
	if err := d.managedGuard(ctx, id); err != nil {
		return ExecResult{}, err
	}
	if len(cmd) == 0 {
		return ExecResult{}, fmt.Errorf("exec command is required")
	}
	info, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return ExecResult{}, err
	}
	if info.State == nil || !info.State.Running {
		return ExecResult{}, fmt.Errorf("container not running")
	}
	execCfg := types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		User:         user,
		Env:          env,
	}
	resp, err := d.c.ContainerExecCreate(ctx, id, execCfg)
	if err != nil {
		return ExecResult{}, err
	}
	attach, err := d.c.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{})
	if err != nil {
		return ExecResult{}, err
	}
	defer attach.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return ExecResult{}, err
	}
	inspect, err := d.c.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return ExecResult{}, err
	}
	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: inspect.ExitCode}
	if inspect.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		if msg == "" {
			msg = "exec failed"
		}
		return res, fmt.Errorf("%s (exit %d)", msg, inspect.ExitCode)
	}
	return res, nil
}

// pickShell returns the most capable interactive shell available in the
// container: bash (full Tab completion + history) is preferred, then zsh,
// falling back to /bin/sh.
func pickShell(ctx context.Context, c client.APIClient, id string) string {
	for _, candidate := range []struct{ path, probe string }{
		{"/bin/bash", "/bin/bash"},
		{"/usr/bin/bash", "/usr/bin/bash"},
		{"/bin/zsh", "/bin/zsh"},
		{"/usr/bin/zsh", "/usr/bin/zsh"},
	} {
		check, err := c.ContainerExecCreate(ctx, id, types.ExecConfig{
			AttachStdout: true,
			AttachStderr: true,
			Cmd:          []string{"test", "-x", candidate.probe},
		})
		if err != nil {
			continue
		}
		attach, err := c.ContainerExecAttach(ctx, check.ID, types.ExecStartCheck{})
		if err != nil {
			continue
		}
		_, _ = io.ReadAll(attach.Reader)
		attach.Close()
		insp, err := c.ContainerExecInspect(ctx, check.ID)
		if err == nil && insp.ExitCode == 0 {
			return candidate.path
		}
	}
	return "/bin/sh"
}

// ResizeExec resizes an active exec session's pty.
func (d *Client) ResizeExec(ctx context.Context, execID string, rows, cols uint) error {
	return d.c.ContainerExecResize(ctx, execID, container.ResizeOptions{Height: rows, Width: cols})
}

// ManagedOwner returns the mudp.user label (the owning username) for a container,
// or "" if the container is not mudp-managed.
func (d *Client) ManagedOwner(ctx context.Context, id string) string {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return ""
	}
	if inspect.Config.Labels["mudp.managed"] != "true" {
		return ""
	}
	return inspect.Config.Labels["mudp.user"]
}

// ContainerName returns the mudp.name label (the display name) for a mudp-managed
// container, or "" if the container is absent or not managed.
func (d *Client) ContainerName(ctx context.Context, id string) string {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return ""
	}
	if inspect.Config.Labels["mudp.managed"] != "true" {
		return ""
	}
	return inspect.Config.Labels["mudp.name"]
}

// ContainerLabel returns a single label value from a mudp-managed container,
// or "" when the container or label is absent.
func (d *Client) ContainerLabel(ctx context.Context, id, key string) (string, error) {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return "", err
	}
	if inspect.Config.Labels == nil {
		return "", nil
	}
	return inspect.Config.Labels[key], nil
}

func (d *Client) Logs(ctx context.Context, id string, tail int) (string, error) {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(strings.TrimPrefix(inspect.Name, "/"), Prefix) || inspect.Config.Labels["mudp.managed"] != "true" {
		return "", fmt.Errorf("container is not managed by mudp")
	}
	if tail <= 0 {
		tail = 200
	}
	rc, err := d.c.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       fmt.Sprintf("%d", tail),
	})
	if err != nil {
		return "", err
	}
	defer rc.Close()

	// mudp containers are created with a TTY, so the logs stream is raw (no
	// 8-byte multiplexed framing header). For TTY-less containers Docker sends
	// a multiplexed stream that must be demuxed via stdcopy. Handling both
	// paths here avoids misinterpreting raw log bytes as a frame header, which
	// previously produced "log frame exceeds maximum size" and corrupted output.
	if inspect.Config.Tty {
		out, err := io.ReadAll(rc)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, rc); err != nil {
		return "", err
	}
	var b strings.Builder
	if stderr.Len() > 0 {
		b.WriteString(stderr.String())
	}
	b.WriteString(stdout.String())
	return b.String(), nil
}

func VolumeMount(path string) mount.Mount {
	return mount.Mount{Type: mount.TypeVolume, Source: path, Target: path}
}

// parseMount turns a "source:target[:ro]" spec into a Docker mount. A source
// starting with "/" or "." is treated as a host bind mount; otherwise it is a
// named volume.
func parseMount(spec string) (mount.Mount, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return mount.Mount{}, fmt.Errorf("mount %q must be source:target[:ro]", spec)
	}
	source := strings.TrimSpace(parts[0])
	target := strings.TrimSpace(parts[1])
	if source == "" || target == "" {
		return mount.Mount{}, fmt.Errorf("mount %q has empty source or target", spec)
	}
	readonly := len(parts) == 3 && strings.TrimSpace(parts[2]) == "ro"
	m := mount.Mount{Target: target, ReadOnly: readonly}
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") {
		m.Type = mount.TypeBind
		m.Source = source
	} else {
		m.Type = mount.TypeVolume
		m.Source = source
	}
	return m, nil
}

// parseDevice turns a "--device" style spec into a Docker DeviceMapping. The spec is
// host[:container[:rwm]]: the host device path, an optional in-container path
// (defaults to the host path), and optional cgroup permissions (rwm by default).
func parseDevice(spec string) (container.DeviceMapping, error) {
	spec = strings.TrimSpace(spec)
	parts := strings.Split(spec, ":")
	if len(parts) < 1 || len(parts) > 3 {
		return container.DeviceMapping{}, fmt.Errorf("device %q must be host[:container[:rwm]]", spec)
	}
	hostPath := strings.TrimSpace(parts[0])
	if hostPath == "" {
		return container.DeviceMapping{}, fmt.Errorf("device %q has no host path", spec)
	}
	containerPath := hostPath
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		containerPath = strings.TrimSpace(parts[1])
	}
	perms := "rwm"
	if len(parts) == 3 {
		perms = strings.TrimSpace(parts[2])
	}
	return container.DeviceMapping{
		PathOnHost:        hostPath,
		PathInContainer:   containerPath,
		CgroupPermissions: perms,
	}, nil
}

// networkingConfig builds the endpoints config for the requested networks. The
// first network becomes the primary endpoint; extras are attached too.
func networkingConfig(names []string) *network.NetworkingConfig {
	cfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	for _, name := range names {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		cfg.EndpointsConfig[n] = &network.EndpointSettings{}
	}
	return cfg
}

// validateMountSource ensures a mount source is either the user's own managed
// volume or an allowed bind. Bind mounts are rejected outright except for the
// netdisk path, which is handled separately via MountNetdisk. Named volumes are
// resolved to their full Docker name and verified to exist and be owned by the
// user, preventing cross-user volume access or host path exposure.
func (d *Client) validateMountSource(ctx context.Context, username string, m *mount.Mount) error {
	source := strings.TrimSpace(m.Source)
	target := strings.TrimSpace(m.Target)
	if source == "" || target == "" {
		return fmt.Errorf("mount has empty source or target")
	}
	if m.Type == mount.TypeBind {
		return fmt.Errorf("bind mount %q is not allowed; use the netdisk option or a managed volume", source)
	}
	full := VolumeFullName(username, source)
	vol, err := d.c.VolumeInspect(ctx, full)
	if err != nil {
		return fmt.Errorf("volume %q not found", source)
	}
	if vol.Labels[ManagedLabel] != "true" || vol.Labels[UserLabel] != username {
		return fmt.Errorf("volume %q is not yours", source)
	}
	m.Source = full
	return nil
}

// validateNetworkAttachment resolves a network name to its full Docker name and
// verifies the user may attach to it: their own mudp-managed networks, plus
// whatever the caller listed in opts.AllowedNetworks — the networks an admin
// granted to one of their groups and the built-in "bridge" when it is open to
// them. This prevents users from attaching to another user's network or to an
// arbitrary host network nobody shared with them.
//
// "bridge" is deliberately not special-cased here: it is grantable like any
// other network now, so whether it may be joined is a question only the caller
// (which knows the grants) can answer.
func (d *Client) validateNetworkAttachment(ctx context.Context, username, name string, allowed map[string]bool) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("network name is empty")
	}
	if IsSystemNetworkName(name) && !allowed[name] {
		return "", fmt.Errorf("network %q is not available to you", name)
	}
	// Allowed networks are already full Docker names and may not be mudp-managed
	// at all, so they only have to exist.
	if allowed[name] {
		if _, err := d.c.NetworkInspect(ctx, name, types.NetworkInspectOptions{}); err != nil {
			return "", fmt.Errorf("network %q not found", name)
		}
		return name, nil
	}
	full := NetworkFullName(username, name)
	info, err := d.c.NetworkInspect(ctx, full, types.NetworkInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("network %q not found", name)
	}
	if info.Labels[ManagedLabel] != "true" || info.Labels[UserLabel] != username {
		return "", fmt.Errorf("network %q is not yours", name)
	}
	return full, nil
}

// LogsStream returns a live-following reader for a container's logs. The caller
// is responsible for closing the reader when done (e.g. on client disconnect).
// tail is the number of historical lines to include before live tailing.
func (d *Client) LogsStream(ctx context.Context, id string, tail string) (io.ReadCloser, error) {
	inspect, err := d.c.ContainerInspect(ctx, id)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.TrimPrefix(inspect.Name, "/"), Prefix) || inspect.Config.Labels[ManagedLabel] != "true" {
		return nil, fmt.Errorf("container is not managed by mudp")
	}
	if tail == "" {
		tail = "200"
	}
	return d.c.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: false,
		Follow:     true,
		Tail:       tail,
	})
}

// StackContainers lists the live containers belonging to a compose project by
// its project name (the com.docker.compose.project label). Used for the stack
// status rollup: compose-managed containers are not mudp-labelled, so they are
// invisible to ListContainers; this queries them directly.
func (d *Client) StackContainers(ctx context.Context, projectName string) ([]Container, error) {
	args := filters.NewArgs()
	args.Add("label", "com.docker.compose.project="+projectName)
	list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(list))
	for _, c := range list {
		full := ""
		if len(c.Names) > 0 {
			full = strings.TrimPrefix(c.Names[0], "/")
		}
		display := c.Labels["com.docker.compose.service"]
		if display == "" {
			display = full
		}
		out = append(out, Container{
			ID: c.ID, Name: display, FullName: full, Image: c.Image, State: c.State, Status: c.Status,
			Labels: c.Labels, CreatedAt: c.Created,
		})
	}
	return out, nil
}

// dequalifyVolumeName strips the mudp-<user>-vol- prefix from a full Docker
// volume name, returning the display name. Returns "" if the name doesn't
// belong to the user's volume namespace -- callers (e.g. DuplicateContainer,
// which may run as an admin duplicating another user's container) rely on
// that empty string to skip a volume outside username's namespace, so
// strings.TrimPrefix alone is wrong here: it returns its input unchanged on a
// non-match instead of "".
func dequalifyVolumeName(full, username string) string {
	prefix := Prefix + Slug(username) + "-vol-"
	if !strings.HasPrefix(full, prefix) {
		return ""
	}
	return strings.TrimPrefix(full, prefix)
}

// dequalifyNetworkName strips the mudp-<user>-net- prefix from a full Docker
// network name, returning the display name. Returns "" on no match, for the
// same reason as dequalifyVolumeName above.
func dequalifyNetworkName(full, username string) string {
	prefix := Prefix + Slug(username) + "-net-"
	if !strings.HasPrefix(full, prefix) {
		return ""
	}
	return strings.TrimPrefix(full, prefix)
}
