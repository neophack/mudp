package dockerx

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
)

// ImageDetail is a richer image record for the admin image list, including
// which containers currently use it.
type ImageDetail struct {
	ID       string   `json:"id"`
	Tags     []string `json:"tags"`
	SizeMB   float64  `json:"sizeMb"`
	Created  int64    `json:"created"`
	InUseBy  int      `json:"inUseBy"`
	Dangling bool     `json:"dangling"`
	MudpTag  bool     `json:"mudpTag"`
}

// ListImagesDetailed returns all local images with usage counts. Used by the
// admin image browser (separate from the published mudp image catalog).
func (d *Client) ListImagesDetailed(ctx context.Context) ([]ImageDetail, error) {
	imgs, err := d.c.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, err
	}
	// Build a set of image IDs referenced by any container for the in-use count.
	containers, _ := d.c.ContainerList(ctx, container.ListOptions{All: true})
	usage := map[string]int{}
	for _, c := range containers {
		usage[c.ImageID]++
	}
	out := make([]ImageDetail, 0, len(imgs))
	for _, im := range imgs {
		tags := im.RepoTags
		if tags == nil {
			tags = []string{"<none>:<none>"}
		}
		dangling := true
		mudp := false
		for _, t := range tags {
			if !strings.Contains(t, "<none>") {
				dangling = false
			}
			if strings.HasPrefix(t, Prefix) {
				mudp = true
			}
		}
		out = append(out, ImageDetail{
			ID:       im.ID,
			Tags:     tags,
			SizeMB:   round2(float64(im.Size) / 1024 / 1024),
			Created:  im.Created,
			InUseBy:  usage[im.ID],
			Dangling: dangling,
			MudpTag:  mudp,
		})
	}
	return out, nil
}

// BuildOptions describes an image build.
type BuildOptions struct {
	Dockerfile   string             // raw Dockerfile body
	Tags         []string           // resulting image tags
	BuildArgs    map[string]*string // --build-arg values
	Auth         string             // base64 registry auth for FROM pulls
	ContextFiles map[string]string  // extra files to include in the build context (name -> body)
	Labels       map[string]string  // labels applied to the built image
	NoCache      bool               // disable Docker layer cache; used for force-rebuilds
}

// BuildImage builds an image from a Dockerfile body, streaming build progress
// (the JSON status lines docker emits) through progress. The build context is
// the Dockerfile plus any ContextFiles.
func (d *Client) BuildImage(ctx context.Context, opts BuildOptions, progress func(line string)) (err error) {
	// Remove any existing image with the same tags before rebuilding so the old
	// image does not become an untagged <none> dangling image. Ignore errors:
	// the image may be in use by a container, in which case Docker will untag it
	// naturally during build.
	for _, tag := range opts.Tags {
		_, _ = d.c.ImageRemove(ctx, tag, image.RemoveOptions{Force: true, PruneChildren: true})
	}
	danglingBefore := d.danglingImageIDs(ctx)
	defer func() {
		if err == nil {
			return
		}
		removed := d.removeNewDanglingImages(danglingBefore)
		if removed > 0 && progress != nil {
			progress(fmt.Sprintf("Cleaned up %d dangling image(s) from failed build", removed))
		}
	}()

	// Pack the Dockerfile (and any extra context files) into a tar build context.
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	writeCtx := func(name string, body string) error {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return err
		}
		return nil
	}
	if err := writeCtx("Dockerfile", opts.Dockerfile); err != nil {
		return err
	}
	for name, body := range opts.ContextFiles {
		if err := writeCtx(name, body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	buildOpts := build.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       opts.Tags,
		BuildArgs:  opts.BuildArgs,
		Labels:     opts.Labels,
		Remove:     true,
		NoCache:    opts.NoCache,
	}
	// Forward registry auth so FROM <private-registry>/... works. The single
	// base64 blob resolves to one registry host; AuthConfigs is keyed by host.
	if opts.Auth != "" {
		if host, ac, ok := decodeAuthForBuild(opts.Auth); ok {
			buildOpts.AuthConfigs = map[string]registry.AuthConfig{host: ac}
		}
	}
	resp, err := d.c.ImageBuild(ctx, bytes.NewReader(buf.Bytes()), buildOpts)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var msg struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		line := scanner.Text()
		if line == "" {
			continue
		}
		if json.Unmarshal([]byte(line), &msg) == nil {
			if msg.Error != "" {
				return fmt.Errorf("build error: %s", msg.Error)
			}
			if msg.Stream != "" {
				if progress != nil {
					progress(strings.TrimRight(msg.Stream, "\n"))
				}
				continue
			}
		}
		if progress != nil {
			progress(line)
		}
	}
	return scanner.Err()
}

func (d *Client) danglingImageIDs(ctx context.Context) map[string]struct{} {
	imgs, err := d.c.ImageList(ctx, image.ListOptions{All: true})
	if err != nil {
		return nil
	}
	ids := make(map[string]struct{}, len(imgs))
	for _, im := range imgs {
		if isDanglingImage(im.RepoTags) {
			ids[im.ID] = struct{}{}
		}
	}
	return ids
}

func isDanglingImage(tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	for _, tag := range tags {
		if !strings.Contains(tag, "<none>") {
			return false
		}
	}
	return true
}

func (d *Client) removeNewDanglingImages(before map[string]struct{}) int {
	if before == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	after := d.danglingImageIDs(ctx)
	removed := 0
	for id := range after {
		if _, existed := before[id]; existed {
			continue
		}
		if _, err := d.c.ImageRemove(ctx, id, image.RemoveOptions{Force: true, PruneChildren: true}); err == nil {
			removed++
		}
	}
	return removed
}

// decodeAuthForBuild reverses a base64 auth blob into the registry host and an
// AuthConfig for build.ImageBuildOptions.AuthConfigs. The host is taken from
// the embedded ServerAddress when present, otherwise callers should key by the
// FROM ref's host themselves; here we return ServerAddress as-is (Docker fills
// it from the ref at build time when empty).
func decodeAuthForBuild(b64 string) (string, registry.AuthConfig, bool) {
	raw, err := base64.URLEncoding.DecodeString(b64)
	if err != nil {
		// Some callers use std encoding.
		raw, err = base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", registry.AuthConfig{}, false
		}
	}
	var ac registry.AuthConfig
	if err := json.Unmarshal(raw, &ac); err != nil {
		return "", registry.AuthConfig{}, false
	}
	host := ac.ServerAddress
	if host == "" {
		host = "docker.io"
	}
	return host, ac, true
}

// ImageExists reports whether an image with the given reference (or ID prefix)
// is present in the local image store.
func (d *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	refs, err := d.c.ImageList(ctx, image.ListOptions{All: true})
	if err != nil {
		return false, err
	}
	for _, im := range refs {
		for _, t := range im.RepoTags {
			if t == ref {
				return true, nil
			}
		}
		if strings.HasPrefix(im.ID, ref) || im.ID == ref {
			return true, nil
		}
	}
	return false, nil
}

// InspectImage returns the full image metadata for a reference. Used to read a
// base image's Entrypoint/Cmd/ID when generating a fused derived image.
func (d *Client) InspectImage(ctx context.Context, ref string) (types.ImageInspect, error) {
	info, _, err := d.c.ImageInspectWithRaw(ctx, ref)
	return info, err
}

// PruneImages removes dangling images. Returns count + bytes reclaimed.
func (d *Client) PruneImages(ctx context.Context) (int, int64, error) {
	args := filters.NewArgs()
	args.Add("dangling", "true")
	report, err := d.c.ImagesPrune(ctx, args)
	if err != nil {
		return 0, 0, err
	}
	return len(report.ImagesDeleted), int64(report.SpaceReclaimed), nil
}

// ManagedImageInfo describes a locally-tagged mudp image discovered by
// ScanManagedImages. It carries the labels needed to reconstruct cache rows.
type ManagedImageInfo struct {
	Ref         string
	BaseRef     string
	BaseImageID string
	CacheKey    string
	ScriptHash  string
	Service     string
	Labels      map[string]string
}

// ScanManagedImages lists all local images tagged with the mudp prefix and
// returns their metadata (base image, cache key, script hash, service). This is
// used on startup to rebuild fused-image/layer cache rows from the Docker image
// store so the Settings page stays in sync across restarts.
func (d *Client) ScanManagedImages(ctx context.Context) ([]ManagedImageInfo, error) {
	imgs, err := d.c.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []ManagedImageInfo
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if !strings.HasPrefix(tag, Prefix) || strings.Contains(tag, "<none>") {
				continue
			}
			info, _, err := d.c.ImageInspectWithRaw(ctx, tag)
			if err != nil {
				continue
			}
			labels := info.Config.Labels
			if labels == nil {
				labels = map[string]string{}
			}
			out = append(out, ManagedImageInfo{
				Ref:         tag,
				BaseRef:     labels["mudp.base"],
				BaseImageID: labels["mudp.baseImageID"],
				CacheKey:    labels["mudp.cacheKey"],
				ScriptHash:  labels["mudp.scriptHash"],
				Service:     labels["mudp.service"],
				Labels:      labels,
			})
		}
	}
	return out, nil
}

// ImportImage imports an image from a tar reader (docker load).
func (d *Client) ImportImage(ctx context.Context, r io.Reader, progress func(line string)) error {
	resp, err := d.c.ImageLoad(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if progress != nil {
			progress(scanner.Text())
		}
	}
	return scanner.Err()
}

// SaveImage streams an image as a tar to the writer (docker save).
func (d *Client) SaveImage(ctx context.Context, ref string, w io.Writer) error {
	rc, err := d.c.ImageSave(ctx, []string{ref})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(w, rc)
	return err
}

// TagImage applies an additional tag to an existing image.
func (d *Client) TagImage(ctx context.Context, src, dst string) error {
	return d.c.ImageTag(ctx, src, dst)
}

// PushImage pushes an image to a registry, streaming progress.
func (d *Client) PushImage(ctx context.Context, ref, auth string, progress func(line string)) error {
	rc, err := d.c.ImagePush(ctx, ref, image.PushOptions{RegistryAuth: auth})
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Status   string `json:"status"`
			ID       string `json:"id"`
			Error    string `json:"error"`
			Progress string `json:"progress"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			if err == context.Canceled {
				return err
			}
			break
		}
		if msg.Error != "" {
			return fmt.Errorf("push error: %s", msg.Error)
		}
		if progress != nil {
			line := msg.Status
			if msg.ID != "" {
				if msg.Progress != "" {
					line = fmt.Sprintf("%s %s %s", msg.ID, msg.Status, msg.Progress)
				} else {
					line = fmt.Sprintf("%s %s", msg.ID, msg.Status)
				}
			}
			progress(line)
		}
	}
	return nil
}
