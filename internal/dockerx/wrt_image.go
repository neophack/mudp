package dockerx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"github.com/docker/docker/api/types"
)

// ErrWRTImageMissing is returned when the ImmortalWrt gateway image is not
// present on the host AND auto-pull is disabled (e.g. the pull just failed and
// we don't want to retry every boot in a tight loop).
var ErrWRTImageMissing = fmt.Errorf(
	"wrt gateway image is missing; " +
		"this is the ImmortalWrt router image. mudp tried to auto-pull it and that failed; " +
		"check the server logs / registry connectivity, or load the image manually via the Images page. " +
		"Container outbound isolation is degraded: containers will have no Internet access " +
		"until the gateway image is available, but LAN/host isolation remains enforced")

// ensureWRTImage verifies the ImmortalWrt gateway image exists locally, and
// when it doesn't, auto-pulls it from the registry. Unlike regular user images
// (which mudp never auto-pulls), the WRT gateway is platform infrastructure:
// an admin opts in by configuring its image on the Networks → WRT card, and mudp
// then keeps that image available on every boot. Pull failures degrade to "no
// outbound Internet" (LAN/host isolation still holds) — they never block boot.
//
// imageRef is policy-driven (default DefaultWRTImage). Boot path — no progress.
func (d *Client) ensureWRTImage(ctx context.Context, imageRef string) error {
	return d.ensureWRTImageWithProgress(ctx, imageRef, nil)
}

// ensureWRTImageWithProgress is the progress-reporting form used by the one-click
// deploy handler: each pull status line is formatted and forwarded to progress
// (nil progress = silent, used at boot). Returns ErrWRTImageMissing on pull
// failure so callers can distinguish "image not pullable" from other errors.
func (d *Client) ensureWRTImageWithProgress(ctx context.Context, imageRef string, progress func(line string)) error {
	if imageRef == "" {
		imageRef = DefaultWRTImage
	}
	if d.imageExistsLocally(ctx, imageRef) {
		if progress != nil {
			progress(fmt.Sprintf("Image %s already present locally.", imageRef))
		}
		return nil
	}
	// Auto-pull the gateway image. This is a public ImmortalWrt image by default;
	// if the admin pointed policy.Image at a private registry they should configure
	// registry creds via the Images page first (we pull anonymously here).
	if progress == nil {
		log.Printf("wrt: image %q not present locally; auto-pulling from registry", imageRef)
	} else {
		progress(fmt.Sprintf("Pulling image %s from registry...", imageRef))
	}
	if err := d.pullImageStream(ctx, imageRef, progress); err != nil {
		return fmt.Errorf("%w (image: %s, pull error: %v)", ErrWRTImageMissing, imageRef, err)
	}
	if progress == nil {
		log.Printf("wrt: image %q pulled successfully", imageRef)
	} else {
		progress(fmt.Sprintf("Image %s pulled successfully.", imageRef))
	}
	return nil
}

// imageExistsLocally reports whether imageRef is present in the local image
// store, matching by repo tag.
func (d *Client) imageExistsLocally(ctx context.Context, imageRef string) bool {
	summaries, err := d.c.ImageList(ctx, types.ImageListOptions{All: true})
	if err != nil {
		// On a listing error we can't confirm presence — let the caller try the
		// pull, which will no-op if the image is actually there.
		return false
	}
	for _, s := range summaries {
		for _, tag := range s.RepoTags {
			if tag == imageRef {
				return true
			}
		}
	}
	return false
}

// pullImageStream pulls imageRef, parsing the JSON-stream response the Docker
// API emits. Each status line is formatted via formatPullLine and forwarded to
// progress (when non-nil); the stream is always drained to EOF so the pull
// completes. Used by both the silent boot path and the progress-reporting
// one-click deploy.
func (d *Client) pullImageStream(ctx context.Context, imageRef string, progress func(line string)) error {
	rc, err := d.c.ImagePull(ctx, imageRef, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("image pull start: %w", err)
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
				return nil
			}
			if err == context.Canceled {
				return err
			}
			return fmt.Errorf("image pull stream: %w", err)
		}
		if msg.Error != "" {
			return fmt.Errorf("pull error: %s", msg.Error)
		}
		if progress != nil {
			progress(formatPullLine(msg.Status, msg.ID, msg.Progress, msg.ProgressDetail.Current, msg.ProgressDetail.Total))
		}
	}
}
