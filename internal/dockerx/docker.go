package dockerx

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"mudp/internal/bootstrap"
)

const Prefix = "mudp-"

// Label keys stamped on every mudp-managed Docker resource (containers,
// volumes, networks). Centralised so backend + UI agree on the vocabulary.
const (
	ManagedLabel = "mudp.managed"
	UserLabel    = "mudp.user"
	NameLabel    = "mudp.name"
)

var cleanPart = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

type Client struct {
	c *client.Client
}

type Container struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	FullName         string            `json:"fullName"`
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
	SSHPort          string            `json:"sshPort,omitempty"`
	SSHUser          string            `json:"sshUser,omitempty"`
	VSCodeURL        string            `json:"vscodeUrl,omitempty"`
	CreatedAt        int64             `json:"createdAt"`
}

type CreateOptions struct {
	Username       string
	Name           string
	ImageRef       string
	ImageName      string
	Env            []string
	GPUs           string
	SSH            bool
	VSCode         bool
	AccessPassword string
	SSHScript      string
	VSCodeScript   string
	Ports          []string
	PortPrefix     int
	// Mounts are bind/named-volume mounts: "source:target[:ro]" entries.
	Mounts       []string
	NetdiskPath  string
	MountNetdisk bool
	// Networks are mudp network names to attach the container to.
	Networks []string
	// RestartPolicy is the Docker restart policy: "no", "always", "unless-stopped",
	// or "on-failure". Defaults to "unless-stopped" when empty.
	RestartPolicy string
	// Progress is an optional callback fired at each creation stage.
	// stage is one of: image, bootstrap, create, copy, start, ssh, vscode, done.
	Progress func(stage, msg string)
	// FusedPlan, when set, requests a fused derived image (base + SSH/VSCode
	// pre-installed) for this create. The server computes the cache key and
	// references; CreateContainer builds the image lazily on a cache miss or
	// reuses it on a hit. If the build fails, CreateContainer falls back to the
	// runtime-injection path. Nil means "use the legacy runtime-injection path".
	FusedPlan *FusedPlan
}

// FusedPlan describes how to build or reuse a fused derived image. The cache
// key ties the image to a specific (base image ID + script bodies + flags)
// combination so admin script edits or base image updates trigger a rebuild.
type FusedPlan struct {
	CacheKey       string   // SHA256(baseImageID + scriptHash + flags)
	FusedRef       string   // local tag, e.g. mudp-fused-<base>-<short>:latest
	BaseRef        string   // the base image reference for FROM
	BaseImageID    string   // immutable Docker image ID of the base
	OrigEntrypoint []string // base image entrypoint, replayed by the runtime script
	OrigCmd        []string // base image cmd, replayed by the runtime script
	// ScriptHash is the hash of the SSH+VSCode script bodies, included in logs
	// and used by the server for cache-key construction.
	ScriptHash string
	// EnableSSH/EnableVSCode mirror the CreateOptions flags at plan time; the
	// fused image is built with exactly these enables.
	EnableSSH    bool
	EnableVSCode bool
	// AccessPassword is baked only as a build placeholder; the real per-container
	// password is applied at runtime via the MUDP_ACCESS_PASSWORD env var.
	AccessPassword string
	SSHScript      string
	VSCodeScript   string
	// Auth is optional base64 registry auth, forwarded to the build so a
	// FROM <private-registry>/... works.
	Auth string
}

// ExecConn wraps a live exec attach used by the WebSocket terminal.
type ExecConn struct {
	Hijacked types.HijackedResponse
	ExecID   string
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
	SSH           bool              `json:"ssh"`
	VSCode        bool              `json:"vscode"`
}

type PortBinding struct {
	Host        string `json:"host"`
	HostPort    string `json:"hostPort"`
	PrivatePort uint16 `json:"privatePort"`
	Type        string `json:"type"`
}

type MountInfo struct {
	Type   string `json:"type"`
	Source string `json:"source"`
	Target string `json:"target"`
	Mode   string `json:"mode"`
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

// MUDPFusedRef builds the local tag for a fused derived image. It combines the
// base image's slug with a short hash of the cache key so multiple base images
// (and rebuilt variants) coexist, and keeps the mudp- prefix so the image shows
// up in managed lists. Example: mudp-fused-ubuntu-3f9a:latest.
func MUDPFusedRef(baseRef, cacheKey string) string {
	short := cacheKey
	if len(short) > 8 {
		short = short[:8]
	}
	return Prefix + "fused-" + Slug(baseRef) + "-" + short + ":latest"
}

// resolveFusedImage returns the image reference to use for a fused create: the
// cached fused ref if the image already exists, or builds it and returns the
// new ref. On any failure it returns "" so the caller falls back to the
// runtime-injection path. Progress is streamed through emit.
func (d *Client) resolveFusedImage(ctx context.Context, plan *FusedPlan, emit func(stage, msg string)) string {
	if plan == nil {
		return ""
	}
	// Cache hit: the fused image is already local.
	if exists, _ := d.ImageExists(ctx, plan.FusedRef); exists {
		emit("bootstrap", "Using cached optimized image "+PublicImageName(plan.FusedRef))
		return plan.FusedRef
	}
	// Cache miss: build it once. This is the slow step that subsequent creates
	// avoid entirely.
	if err := d.buildFused(ctx, plan, emit); err != nil {
		emit("bootstrap", "Optimized image build failed ("+err.Error()+"); falling back to runtime install")
		return ""
	}
	return plan.FusedRef
}

// BuildFusedImage unconditionally (re)builds the fused derived image described
// by plan, streaming each build line through emit. Used by the manual admin
// "Build SSH/VSCode Image" action so a Rebuild actually rebuilds even when a
// cached image exists. Returns an error if the build fails.
func (d *Client) BuildFusedImage(ctx context.Context, plan *FusedPlan, emit func(stage, msg string)) error {
	if plan == nil {
		return fmt.Errorf("no fused plan")
	}
	return d.buildFused(ctx, plan, emit)
}

// buildFused is the shared core: it assembles the fused build context, drives
// `docker build`, and tags the result. emit receives each stage message and
// build line. Both the lazy create path (resolveFusedImage) and the explicit
// admin build (BuildFusedImage) go through here.
func (d *Client) buildFused(ctx context.Context, plan *FusedPlan, emit func(stage, msg string)) error {
	emit("bootstrap", "Building optimized image (one-time, may take a few minutes)…")
	buildCtx, err := bootstrap.FusedContext(bootstrap.Config{
		EnableSSH:      plan.EnableSSH,
		EnableVSCode:   plan.EnableVSCode,
		AccessPassword: plan.AccessPassword,
		SSHScript:      plan.SSHScript,
		VSCodeScript:   plan.VSCodeScript,
		OrigEntrypoint: plan.OrigEntrypoint,
		OrigCmd:        plan.OrigCmd,
		BaseRef:        plan.BaseRef,
	})
	if err != nil {
		return fmt.Errorf("prepare build context: %w", err)
	}
	// BuildImage packs a Dockerfile + named context files into its own tar, so
	// unpack the fused context tar we just built into those two pieces.
	dockerfile, contextFiles := unpackFusedContext(buildCtx)
	labels := map[string]string{
		"mudp.fused":      "true",
		"mudp.base":       plan.BaseRef,
		"mudp.cacheKey":   plan.CacheKey,
		"mudp.scriptHash": plan.ScriptHash,
	}
	if err := d.BuildImage(ctx, BuildOptions{
		Dockerfile:   dockerfile,
		Tags:         []string{plan.FusedRef},
		ContextFiles: contextFiles,
		Labels:       labels,
		Auth:         plan.Auth,
	}, func(line string) { emit("bootstrap", line) }); err != nil {
		return err
	}
	emit("bootstrap", "Optimized image ready")
	return nil
}

// unpackFusedContext reads a fused build-context tar (as produced by
// bootstrap.FusedContext) and returns the Dockerfile body plus a name→body map
// of the other entries, suitable for BuildImage.
func unpackFusedContext(buf *bytes.Buffer) (string, map[string]string) {
	tr := tar.NewReader(buf)
	dockerfile := ""
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			break
		}
		if hdr.Name == "Dockerfile" {
			dockerfile = string(body)
		} else {
			files[hdr.Name] = string(body)
		}
	}
	return dockerfile, files
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
	rc, err := d.c.ImagePull(ctx, sourceRef, image.PullOptions{})
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
	_, err := d.c.ImageRemove(ctx, ref, image.RemoveOptions{Force: true, PruneChildren: true})
	return err
}

func (d *Client) ListManagedImages(ctx context.Context) ([]string, error) {
	items, err := d.c.ImageList(ctx, image.ListOptions{})
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
	if (opts.SSH || opts.VSCode) && strings.TrimSpace(opts.AccessPassword) == "" {
		return "", fmt.Errorf("access password is required when SSH or VS Code is enabled")
	}
	name := FullContainerName(opts.Username, opts.Name)
	emit("image", "Inspecting image "+opts.ImageRef)
	imageInfo, _, err := d.c.ImageInspectWithRaw(ctx, opts.ImageRef)
	if err != nil {
		return "", err
	}
	if (opts.SSH || opts.VSCode) && imageInfo.Os != "" && imageInfo.Os != "linux" {
		return "", fmt.Errorf("SSH and VS Code bootstrap are only supported for Linux containers")
	}
	exposed := nat.PortSet{}
	portMap := nat.PortMap{}
	usedPorts, err := d.managedHostPorts(ctx)
	if err != nil {
		return "", err
	}
	allocated := map[int]bool{}
	addHostPort := func(hostPort int) error {
		if opts.PortPrefix > 0 && (hostPort < opts.PortPrefix*100 || hostPort > opts.PortPrefix*100+99) {
			return fmt.Errorf("host port %d is outside your assigned range %d00-%d99", hostPort, opts.PortPrefix, opts.PortPrefix)
		}
		if hostPort < 10000 {
			return fmt.Errorf("host port %d is reserved; use your assigned range", hostPort)
		}
		if usedPorts[hostPort] || allocated[hostPort] {
			return fmt.Errorf("host port %d is already allocated", hostPort)
		}
		allocated[hostPort] = true
		return nil
	}
	nextPort := func() (string, error) {
		if opts.PortPrefix <= 0 {
			return "", fmt.Errorf("port prefix is not assigned")
		}
		start := opts.PortPrefix * 100
		for p := start; p <= start+99 && p <= 65535; p++ {
			if p < 10000 {
				continue
			}
			if !usedPorts[p] && !allocated[p] && portFree(p) {
				allocated[p] = true
				return strconv.Itoa(p), nil
			}
		}
		return "", fmt.Errorf("no free ports in assigned range %d-%d", start, start+99)
	}
	addPort := func(spec string) error {
		if spec == "" {
			return nil
		}
		parts := strings.Split(spec, ":")
		if len(parts) != 2 {
			return fmt.Errorf("port %q must be host:container", spec)
		}
		hostPort, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return fmt.Errorf("invalid host port %q", parts[0])
		}
		if err := addHostPort(hostPort); err != nil {
			return err
		}
		p := nat.Port(parts[1] + "/tcp")
		exposed[p] = struct{}{}
		portMap[p] = []nat.PortBinding{{HostPort: strconv.Itoa(hostPort)}}
		return nil
	}
	for _, p := range opts.Ports {
		if err := addPort(strings.TrimSpace(p)); err != nil {
			return "", err
		}
	}
	if opts.SSH {
		hostPort, err := nextPort()
		if err != nil {
			return "", err
		}
		exposed["22/tcp"] = struct{}{}
		portMap["22/tcp"] = []nat.PortBinding{{HostPort: hostPort}}
	}
	if opts.VSCode {
		hostPort, err := nextPort()
		if err != nil {
			return "", err
		}
		exposed["13337/tcp"] = struct{}{}
		portMap["13337/tcp"] = []nat.PortBinding{{HostPort: hostPort}}
	}
	labels := map[string]string{
		ManagedLabel:          "true",
		UserLabel:             opts.Username,
		NameLabel:             opts.Name,
		"mudp.image":          opts.ImageName,
		"mudp.gpu":            opts.GPUs,
		"mudp.ssh":            fmt.Sprint(opts.SSH),
		"mudp.vscode":         fmt.Sprint(opts.VSCode),
		"mudp.connectionUser": "root",
	}
	hostCfg := &container.HostConfig{PortBindings: portMap, RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(normalizeRestartPolicy(opts.RestartPolicy))}}
	// Mounts: parse "source:target[:ro]" into bind or volume mounts. Sources
	// starting with "/" or "." are binds; everything else is a named volume.
	// Only the user's own managed volumes and the netdisk bind are allowed.
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
	resolvedNetworks := make([]string, 0, len(opts.Networks))
	for _, n := range opts.Networks {
		full, err := d.validateNetworkAttachment(ctx, opts.Username, n)
		if err != nil {
			return "", err
		}
		resolvedNetworks = append(resolvedNetworks, full)
	}
	if opts.MountNetdisk && strings.TrimSpace(opts.NetdiskPath) != "" {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: strings.TrimSpace(opts.NetdiskPath),
			Target: "/netdisk",
		})
	}
	if opts.GPUs != "" && opts.GPUs != "none" {
		hostCfg.Resources.DeviceRequests = []container.DeviceRequest{{
			Driver:       "nvidia",
			Count:        -1,
			Capabilities: [][]string{{"gpu"}},
		}}
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
	if opts.SSH || opts.VSCode {
		// Try the fused derived-image path first: build-or-reuse a pre-installed
		// image so container start skips the slow per-boot install. Falls back to
		// runtime injection below if the build fails or no plan was supplied.
		if fusedRef := d.resolveFusedImage(ctx, opts.FusedPlan, emit); fusedRef != "" {
			// The fused image already has SSH/VSCode installed and its ENTRYPOINT
			// runs the per-boot runtime script. Supply the access password via env.
			containerCfg.Image = fusedRef
			containerCfg.Env = append(containerCfg.Env, "MUDP_ACCESS_PASSWORD="+opts.AccessPassword)
			// No entrypoint override, no CopyToContainer — the fused image is self-contained.
			emit("create", "Creating container")
			resp, err := d.c.ContainerCreate(ctx, containerCfg, hostCfg,
				networkingConfig(resolvedNetworks), &v1.Platform{}, name)
			if err != nil {
				return "", err
			}
			emit("start", "Starting container")
			if err := d.c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
				_ = d.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
				return "", err
			}
			if opts.SSH {
				emit("ssh", "Waiting for SSH to come up")
				if err := d.waitForReady(ctx, resp.ID, 22); err != nil {
					emit("ssh", "SSH not ready yet — the link will appear once it is up")
				} else {
					emit("ssh", "SSH ready")
				}
			}
			if opts.VSCode {
				emit("vscode", "Waiting for VS Code Server to come up")
				if err := d.waitForReady(ctx, resp.ID, 13337); err != nil {
					emit("vscode", "VS Code not ready yet — the link will appear once it is up")
				} else {
					emit("vscode", "VS Code ready")
				}
			}
			emit("done", "Container created")
			return resp.ID, nil
		}
		// Fallback: runtime injection of the bootstrap scripts.
		emit("bootstrap", "Generating bootstrap scripts")
		payload, err := bootstrap.Tarball(bootstrap.Config{
			EnableSSH:      opts.SSH,
			EnableVSCode:   opts.VSCode,
			AccessPassword: opts.AccessPassword,
			SSHScript:      opts.SSHScript,
			VSCodeScript:   opts.VSCodeScript,
			OrigEntrypoint: imageInfo.Config.Entrypoint,
			OrigCmd:        imageInfo.Config.Cmd,
		})
		if err != nil {
			return "", err
		}
		containerCfg.Entrypoint = []string{"/bin/sh", "/mudp-bootstrap/entrypoint.sh"}
		emit("create", "Creating container")
		resp, err := d.c.ContainerCreate(ctx, containerCfg,
			hostCfg,
			networkingConfig(resolvedNetworks),
			&v1.Platform{},
			name,
		)
		if err != nil {
			return "", err
		}
		emit("copy", "Injecting bootstrap files")
		if err := d.c.CopyToContainer(ctx, resp.ID, "/", payload, container.CopyToContainerOptions{}); err != nil {
			_ = d.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
			return "", err
		}
		emit("start", "Starting container")
		if err := d.c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			_ = d.c.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
			return "", err
		}
		if opts.SSH {
			emit("ssh", "Waiting for SSH to come up")
			if err := d.waitForReady(ctx, resp.ID, 22); err != nil {
				emit("ssh", "SSH not ready yet — install may still be running; the link will appear once it is up")
			} else {
				emit("ssh", "SSH ready")
			}
		}
		if opts.VSCode {
			emit("vscode", "Waiting for VS Code Server to come up")
			if err := d.waitForReady(ctx, resp.ID, 13337); err != nil {
				emit("vscode", "VS Code not ready yet — install may still be running; the link will appear once it is up")
			} else {
				emit("vscode", "VS Code ready")
			}
		}
		emit("done", "Container created")
		return resp.ID, nil
	}
	emit("create", "Creating container")
	resp, err := d.c.ContainerCreate(ctx,
		containerCfg,
		hostCfg,
		networkingConfig(resolvedNetworks),
		&v1.Platform{},
		name,
	)
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
	ContainerID string  `json:"containerId"`
	Container   string  `json:"container"`
	User        string  `json:"user"`
	PID         string  `json:"pid"`
	CPUPercent  float64 `json:"cpuPct"`
	MemoryMB    float64 `json:"memMb"`
	Command     string  `json:"command"`
}

type GPUUsage struct {
	Percent       float64 `json:"gpuPct"`
	MemoryMB      float64 `json:"gpuMemMb"`
	MemoryTotalMB float64 `json:"gpuMemTotalMb"`
	MemoryPct     float64 `json:"gpuMemPct"`
}

type gpuMetric struct {
	Index         string
	Percent       float64
	MemoryMB      float64
	MemoryTotalMB float64
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
		usage.MemoryTotalMB += m.MemoryTotalMB
	}
	usage.Percent = round2(usage.Percent / float64(len(selected)))
	if usage.MemoryTotalMB > 0 {
		usage.MemoryPct = round2(usage.MemoryMB / usage.MemoryTotalMB * 100)
	}
	usage.MemoryMB = round2(usage.MemoryMB)
	usage.MemoryTotalMB = round2(usage.MemoryTotalMB)
	return usage, nil
}

func parseGPUMetrics(raw string) []gpuMetric {
	var out []gpuMetric
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		pct, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		mem, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		total, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
		out = append(out, gpuMetric{
			Index:         strings.TrimSpace(parts[0]),
			Percent:       pct,
			MemoryMB:      mem,
			MemoryTotalMB: total,
		})
	}
	return out
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
		top, err := d.c.ContainerTop(ctx, c.ID, []string{"aux"})
		if err != nil || len(top.Processes) == 0 {
			continue
		}
		idx := map[string]int{}
		for i, t := range top.Titles {
			idx[strings.ToUpper(t)] = i
		}
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
			cmd := get("COMMAND", "CMD")
			out = append(out, TopProcess{
				ContainerID: c.ID,
				Container:   c.Name,
				User:        c.Labels[UserLabel],
				PID:         get("PID"),
				CPUPercent:  cpu,
				MemoryMB:    memPct,
				Command:     cmd,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUPercent > out[j].CPUPercent })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func (d *Client) SetAccessPassword(ctx context.Context, id, password string) error {
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("password is required")
	}
	if err := d.managedGuard(ctx, id); err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("root:" + password + "\n"))
	cmd := fmt.Sprintf("printf %%s %s | base64 -d | chpasswd", encoded)
	execCfg := container.ExecOptions{AttachStdout: true, AttachStderr: true, Cmd: []string{"/bin/sh", "-lc", cmd}}
	resp, err := d.c.ContainerExecCreate(ctx, id, execCfg)
	if err != nil {
		return err
	}
	attach, err := d.c.ContainerExecAttach(ctx, resp.ID, container.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer attach.Close()
	_, _ = io.Copy(io.Discard, attach.Reader)
	return nil
}

// readyMarkerFor maps a service's private port to the bootstrap marker file
// the entrypoint touches once its install script ran to completion (under
// set -eu, so the marker only appears on success — see bootstrap.go).
var readyMarkerFor = map[uint16]string{
	22:    "/tmp/mudp/ssh.ready",
	13337: "/tmp/mudp/vscode.ready",
}

// errNotReady is returned by waitForReady when the service does not come up
// before the deadline. Callers treat it as advisory (the container is kept).
var errNotReady = errors.New("service did not become ready in time")

// waitForReady blocks until the bootstrap install for privatePort has finished
// (its marker file exists in the container) AND the published host port accepts
// a TCP connection. code-server's network install can take minutes, so the
// deadline is generous. Returns errNotReady on timeout; the caller keeps the
// container either way and surfaces a message to the user.
func (d *Client) waitForReady(ctx context.Context, id string, privatePort uint16) error {
	marker := readyMarkerFor[privatePort]
	deadline, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for {
		if deadline.Err() != nil {
			return errNotReady
		}
		if d.markerExists(ctx, id, marker) {
			if hostPort := d.publishedHostPort(ctx, id, privatePort); hostPort != "" {
				if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+hostPort, 2*time.Second); err == nil {
					_ = conn.Close()
					return nil
				}
			}
		}
		select {
		case <-deadline.Done():
			return errNotReady
		case <-time.After(3 * time.Second):
		}
	}
}

// markerExists runs `test -f <marker>` inside the container and reports whether
// the marker file is present (i.e. the bootstrap install script completed).
func (d *Client) markerExists(ctx context.Context, id, marker string) bool {
	if marker == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	execCfg := container.ExecOptions{Cmd: []string{"/bin/sh", "-c", "test -f " + marker}}
	resp, err := d.c.ContainerExecCreate(probeCtx, id, execCfg)
	if err != nil {
		return false
	}
	// Attach to drive the exec to completion, then read its exit code.
	attach, err := d.c.ContainerExecAttach(probeCtx, resp.ID, container.ExecAttachOptions{})
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, attach.Reader)
	attach.Close()
	inspect, err := d.c.ContainerExecInspect(probeCtx, resp.ID)
	if err != nil {
		return false
	}
	return inspect.ExitCode == 0
}

// publishedHostPort returns the host port Docker bound to the given container
// private port (e.g. 22 -> "2222"), or "" if none is published yet.
func (d *Client) publishedHostPort(ctx context.Context, id string, privatePort uint16) string {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inspect, err := d.c.ContainerInspect(probeCtx, id)
	if err != nil {
		return ""
	}
	target := nat.Port(fmt.Sprintf("%d/tcp", privatePort))
	for port, bindings := range inspect.NetworkSettings.Ports {
		if port != target {
			continue
		}
		for _, pb := range bindings {
			if pb.HostPort != "" {
				return pb.HostPort
			}
		}
	}
	return ""
}

// serviceReady is a lightweight, list-time readiness check: it reports whether
// a service's bootstrap marker exists in the container. Used by ListContainers
// to show SSH/VSCode links only once the install has actually completed.
func (d *Client) serviceReady(ctx context.Context, id string, privatePort uint16) bool {
	return d.markerExists(ctx, id, readyMarkerFor[privatePort])
}

func (d *Client) ListContainers(ctx context.Context, username string, admin bool) ([]Container, error) {
	args := filters.NewArgs(filters.Arg("label", "mudp.managed=true"))
	list, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Size: true, Filters: args})
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
		ports := make([]string, 0, len(c.Ports))
		seenPorts := map[string]bool{}
		for _, p := range c.Ports {
			var rendered string
			if p.PublicPort > 0 {
				rendered = fmt.Sprintf("%d:%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			} else {
				rendered = fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			}
			if !seenPorts[rendered] {
				seenPorts[rendered] = true
				ports = append(ports, rendered)
			}
		}
		out = append(out, Container{
			ID: c.ID, Name: display, FullName: full, Image: c.Labels["mudp.image"], State: c.State, Status: c.Status,
			Ports: ports, Labels: c.Labels, DiskMB: float64(c.SizeRw) / 1024 / 1024, GPU: c.Labels["mudp.gpu"], CreatedAt: c.Created,
			SSHPort: mappedPort(c.Ports, 22), SSHUser: sshUser(c.Labels), VSCodeURL: vscodeURL(c.Ports),
		})
	}
	for i := range out {
		mem, _ := d.memoryMB(ctx, out[i].ID)
		out[i].MemoryMB = mem
		if gpu, _ := d.GPUUsage(ctx, out[i].GPU); gpu.MemoryTotalMB > 0 || gpu.Percent > 0 {
			out[i].GPUPercent = gpu.Percent
			out[i].GPUMemoryMB = gpu.MemoryMB
			out[i].GPUMemoryTotalMB = gpu.MemoryTotalMB
			out[i].GPUMemoryPct = gpu.MemoryPct
		}
		// Only surface SSH/VSCode connection info once the bootstrap install
		// has actually finished (its marker file is present), so users never
		// get a link to a service that isn't listening yet.
		if out[i].SSHPort != "" && !d.serviceReady(ctx, out[i].ID, 22) {
			out[i].SSHPort = ""
			out[i].SSHUser = ""
		}
		if out[i].VSCodeURL != "" && !d.serviceReady(ctx, out[i].ID, 13337) {
			out[i].VSCodeURL = ""
		}
	}
	return out, nil
}

func mappedPort(ports []container.Port, private uint16) string {
	for _, p := range ports {
		if p.PrivatePort == private && p.PublicPort > 0 {
			return fmt.Sprintf("%d", p.PublicPort)
		}
	}
	return ""
}

func sshUser(labels map[string]string) string {
	if labels["mudp.ssh"] == "true" {
		if user := labels["mudp.connectionUser"]; user != "" {
			return user
		}
		return "root"
	}
	return ""
}

func vscodeURL(ports []container.Port) string {
	for _, p := range ports {
		if p.PrivatePort == 13337 && p.PublicPort > 0 {
			host := p.IP
			if host == "" || host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
			return fmt.Sprintf("http://%s:%d", host, p.PublicPort)
		}
	}
	return ""
}

func (d *Client) memoryMB(ctx context.Context, id string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := d.c.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var s struct {
		MemoryStats struct {
			Usage uint64 `json:"usage"`
			Stats struct {
				Cache uint64 `json:"cache"`
			} `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return 0, err
	}
	// Report the working set (usage minus page cache) so the listing matches the
	// value shown in the live stats stream.
	usage := s.MemoryStats.Usage - s.MemoryStats.Stats.Cache
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
	case "":
		return "unless-stopped"
	default:
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
		SSH:           inspect.Config.Labels["mudp.ssh"] == "true",
		VSCode:        inspect.Config.Labels["mudp.vscode"] == "true",
		ImageName:     inspect.Config.Labels["mudp.image"],
	}
	if info.Labels == nil {
		info.Labels = map[string]string{}
	}
	if t, err := time.Parse(time.RFC3339Nano, inspect.Created); err == nil {
		info.CreatedAt = t.Unix()
	}
	for port, bindings := range inspect.NetworkSettings.Ports {
		portType := port.Proto()
		privatePort := port.Int()
		if len(bindings) == 0 {
			info.Ports = append(info.Ports, PortBinding{PrivatePort: uint16(privatePort), Type: portType})
			continue
		}
		for _, b := range bindings {
			info.Ports = append(info.Ports, PortBinding{Host: b.HostIP, HostPort: b.HostPort, PrivatePort: uint16(privatePort), Type: portType})
		}
	}
	for _, m := range inspect.Mounts {
		info.Mounts = append(info.Mounts, MountInfo{Type: string(m.Type), Source: m.Source, Target: m.Destination, Mode: m.Mode})
	}
	for name, net := range inspect.NetworkSettings.Networks {
		info.Networks = append(info.Networks, NetworkInfo{Name: name, IPAddress: net.IPAddress, Gateway: net.Gateway, MacAddress: net.MacAddress})
		if info.IPAddress == "" && net.IPAddress != "" {
			info.IPAddress = net.IPAddress
		}
	}
	return info, nil
}

// ExecAttach opens an interactive exec session attached to a pty, returning the
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
	execCfg := container.ExecOptions{
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
	attachResp, err := d.c.ContainerExecAttach(ctx, createResp.ID, container.ExecAttachOptions{Tty: true, ConsoleSize: &[2]uint{rows, cols}})
	if err != nil {
		return ExecConn{}, err
	}
	return ExecConn{Hijacked: attachResp, ExecID: createResp.ID}, nil
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
		check, err := c.ContainerExecCreate(ctx, id, container.ExecOptions{
			AttachStdout: true,
			AttachStderr: true,
			Cmd:          []string{"test", "-x", candidate.probe},
		})
		if err != nil {
			continue
		}
		attach, err := c.ContainerExecAttach(ctx, check.ID, container.ExecAttachOptions{})
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
	reader := bufio.NewReader(rc)
	var b strings.Builder
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return "", err
		}
		size := int(header[4])<<24 | int(header[5])<<16 | int(header[6])<<8 | int(header[7])
		if size <= 0 {
			continue
		}
		const maxLogFrame = 16 << 20 // 16 MiB per log frame
		if size > maxLogFrame {
			return "", fmt.Errorf("log frame exceeds maximum size")
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return "", err
		}
		b.Write(payload)
	}
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
// verifies it is managed by mudp and owned by the user. This prevents users from
// attaching to another user's network or to unmanaged system networks.
func (d *Client) validateNetworkAttachment(ctx context.Context, username, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("network name is empty")
	}
	full := NetworkFullName(username, name)
	info, err := d.c.NetworkInspect(ctx, full, network.InspectOptions{})
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
