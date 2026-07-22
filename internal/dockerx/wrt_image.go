package dockerx

import (
	"context"
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
// imageRef is policy-driven (default DefaultWRTImage).
func (d *Client) ensureWRTImage(ctx context.Context, imageRef string) error {
	if imageRef == "" {
		imageRef = DefaultWRTImage
	}
	if d.imageExistsLocally(ctx, imageRef) {
		return nil
	}
	// Auto-pull the gateway image. This is a public ImmortalWrt image by default;
	// if the admin pointed policy.Image at a private registry they should configure
	// registry creds via the Images page first (we pull anonymously here).
	log.Printf("wrt: image %q not present locally; auto-pulling from registry", imageRef)
	if err := d.pullImageLogged(ctx, imageRef); err != nil {
		return fmt.Errorf("%w (image: %s, pull error: %v)", ErrWRTImageMissing, imageRef, err)
	}
	log.Printf("wrt: image %q pulled successfully", imageRef)
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

// pullImageLogged pulls imageRef, draining the JSON-stream response the Docker
// API emits (required even when you don't care about progress — otherwise the
// pull can hang). Errors are surfaced to the caller; per-layer progress lines
// are logged at debug-equivalent verbosity (one summary line).
func (d *Client) pullImageLogged(ctx context.Context, imageRef string) error {
	rc, err := d.c.ImagePull(ctx, imageRef, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("image pull start: %w", err)
	}
	defer rc.Close()
	// Drain the stream. We don't parse every status line, but we must read to
	// EOF so the pull completes; the Docker daemon only finishes the pull once
	// the response stream is fully consumed.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("image pull stream: %w", err)
	}
	return nil
}
