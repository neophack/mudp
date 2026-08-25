package server

import (
	"bytes"
	crand "crypto/rand"
	"encoding/hex"
	"image"
	"image/color"
	"image/gif"
	"math"
	"math/big"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Login captcha: an animated GIF challenge plus an in-memory answer store.
// The answer never leaves the server; the client only holds the opaque id.
// Entries are single-use (consumed by one verify, success or failure) and
// expire after captchaTTL, so a captured image cannot be replayed.

const (
	captchaTTL     = 5 * time.Minute
	captchaLength  = 5
	captchaWidth   = 160
	captchaHeight  = 48
	captchaFrames  = 12
	captchaFrameMs = 100
	// Ambiguous glyphs (0/O, 1/I, lowercase l) are excluded so humans never
	// have to guess which one the image means.
	captchaCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

type captchaEntry struct {
	answer string
	expiry time.Time
}

type captchaStore struct {
	mu      sync.Mutex
	entries map[string]captchaEntry
}

func newCaptchaStore() *captchaStore {
	return &captchaStore{entries: make(map[string]captchaEntry)}
}

func (s *captchaStore) set(id, answer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, e := range s.entries {
		if now.After(e.expiry) {
			delete(s.entries, k)
		}
	}
	s.entries[id] = captchaEntry{answer: answer, expiry: now.Add(captchaTTL)}
}

// verify compares case-insensitively and consumes the entry either way, so a
// wrong guess (or a replay of a correct one) cannot keep trying the same id.
func (s *captchaStore) verify(id, code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	delete(s.entries, id)
	return ok && time.Now().Before(e.expiry) && strings.EqualFold(e.answer, code)
}

func randomCaptchaText() string {
	out := make([]byte, captchaLength)
	max := big.NewInt(int64(len(captchaCharset)))
	for i := range out {
		n, err := crand.Int(crand.Reader, max)
		if err != nil {
			// crypto/rand never fails in practice; any fallback must still be
			// unpredictable enough for a 5-minute-lived challenge.
			panic("captcha: crypto/rand unavailable: " + err.Error())
		}
		out[i] = captchaCharset[n.Int64()]
	}
	return string(out)
}

// drawCaptchaFrame renders one frame of the challenge. Each glyph rides its
// own sine wave (phase offsets from the frame index) and the noise lines are
// re-randomized per frame, so no single static frame carries the full text
// cleanly — that motion is the point of a GIF captcha.
func drawCaptchaFrame(img *image.Paletted, text string, frame int) {
	drawer := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(color.RGBA{R: 20, G: 20, B: 25, A: 255}),
		Face: basicfont.Face7x13,
	}
	slot := (captchaWidth - 12) / len(text)
	for i, ch := range text {
		phase := float64(frame)/float64(captchaFrames)*2*math.Pi + float64(i)*1.7
		yOff := math.Sin(phase) * 8
		x := 8 + i*slot + rand.IntN(slot/2)
		y := captchaHeight/2 + 5 + int(yOff)
		drawer.Dot = fixed.P(x, y)
		drawer.DrawString(string(ch))
	}
	// Per-frame noise: a few long lines plus scattered dots.
	for l := 0; l < 3; l++ {
		drawNoiseLine(img)
	}
	for d := 0; d < 40; d++ {
		img.SetColorIndex(rand.IntN(captchaWidth), rand.IntN(captchaHeight), uint8(rand.IntN(4))+1)
	}
}

func drawNoiseLine(img *image.Paletted) {
	x0 := rand.Float64() * float64(captchaWidth)
	y0 := rand.Float64() * float64(captchaHeight)
	angle := rand.Float64() * 2 * math.Pi
	length := 40 + rand.Float64()*90
	idx := uint8(rand.IntN(4)) + 1
	for t := 0; t <= int(length); t++ {
		x := int(x0 + math.Cos(angle)*float64(t))
		y := int(y0 + math.Sin(angle)*float64(t)*0.6)
		if x >= 0 && x < captchaWidth && y >= 0 && y < captchaHeight {
			img.SetColorIndex(x, y, idx)
		}
	}
}

// renderCaptchaGIF encodes the animated challenge. One palette serves every
// frame; colors 1..4 are the glyph/noise tones on a white background.
func renderCaptchaGIF(text string) ([]byte, error) {
	palette := color.Palette{
		color.RGBA{R: 255, G: 255, B: 255, A: 255}, // 0 background
		color.RGBA{R: 60, G: 64, B: 70, A: 255},    // 1
		color.RGBA{R: 130, G: 136, B: 145, A: 255}, // 2
		color.RGBA{R: 30, G: 110, B: 210, A: 255},  // 3 brand-tinted noise
		color.RGBA{R: 200, G: 90, B: 60, A: 255},   // 4
	}
	out := &gif.GIF{}
	for f := 0; f < captchaFrames; f++ {
		img := image.NewPaletted(image.Rect(0, 0, captchaWidth, captchaHeight), palette)
		drawCaptchaFrame(img, text, f)
		out.Image = append(out.Image, img)
		out.Delay = append(out.Delay, captchaFrameMs/10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// captchaHandler serves a fresh animated GIF. The challenge id rides a
// response header because the body is the binary image; the login form reads
// both via fetch(). With CaptchaTestAnswers enabled the answer is also
// exposed in a header — strictly for automated tests that cannot OCR.
func (a *App) captchaHandler(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 16)
	if _, err := crand.Read(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create captcha")
		return
	}
	id := hex.EncodeToString(raw)
	answer := randomCaptchaText()
	a.captchas.set(id, answer)
	gifBytes, err := renderCaptchaGIF(answer)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to render captcha")
		return
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Mudp-Captcha-Id", id)
	if a.cfg.CaptchaTestAnswers {
		w.Header().Set("X-Mudp-Captcha-Answer", answer)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(gifBytes)))
	_, _ = w.Write(gifBytes)
}
