package dockerx

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

// ImageDetail is a richer image record for the admin image list, including
// which containers currently use it.
type ImageDetail struct {
	ID        string   `json:"id"`
	Tags      []string `json:"tags"`
	SizeMB    float64  `json:"sizeMb"`
	Created   int64    `json:"created"`
	InUseBy   int      `json:"inUseBy"`
	Dangling  bool     `json:"dangling"`
	MudpTag   bool     `json:"mudpTag"`
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
	Dockerfile string            // raw Dockerfile body
	Tags       []string          // resulting image tags
	BuildArgs  map[string]*string // --build-arg values
	Auth       string            // base64 registry auth for FROM pulls
}

// BuildImage builds an image from a Dockerfile body, streaming build progress
// (the JSON status lines docker emits) through progress.
func (d *Client) BuildImage(ctx context.Context, opts BuildOptions, progress func(line string)) error {
	// Pack the Dockerfile into a tar build context.
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	hdr := &tar.Header{Name: "Dockerfile", Mode: 0644, Size: int64(len(opts.Dockerfile))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(opts.Dockerfile)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	resp, err := d.c.ImageBuild(ctx, bytes.NewReader(buf.Bytes()), build.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       opts.Tags,
		BuildArgs:  opts.BuildArgs,
		Remove:     true,
	})
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
