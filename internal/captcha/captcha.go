// Package captcha generates image-based CAPTCHAs with interference stripes.
package captcha

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	mrand "math/rand"
	"sync"
	"time"
)

type entry struct {
	answer    string
	expiresAt time.Time
}

var (
	store = make(map[string]entry)
	mu    sync.Mutex
)

// Response holds the captcha ID and base64-encoded PNG image.
type Response struct {
	ID    string `json:"id"`
	Image string `json:"image"` // data:image/png;base64,...
}

// chars used in captcha text (no ambiguous chars like 0/O, 1/l/I)
const chars = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// Generate creates a new image captcha and returns its ID + base64 PNG.
func Generate() *Response {
	mu.Lock()
	defer mu.Unlock()

	// Clean expired
	now := time.Now()
	for k, v := range store {
		if now.After(v.expiresAt) {
			delete(store, k)
		}
	}

	// Generate random 4-char text
	text := make([]byte, 4)
	for i := range text {
		text[i] = chars[mrand.Intn(len(chars))]
	}
	answer := string(text)

	id := generateCaptchaID()
	store[id] = entry{
		answer:    answer,
		expiresAt: now.Add(5 * time.Minute),
	}

	img := renderCaptcha(answer, 160, 50)

	var buf bytes.Buffer
	png.Encode(&buf, img)
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	return &Response{
		ID:    id,
		Image: "data:image/png;base64," + b64,
	}
}

// Validate checks the answer (case-insensitive) and consumes the captcha.
func Validate(id, answer string) bool {
	mu.Lock()
	defer mu.Unlock()

	e, ok := store[id]
	if !ok {
		return false
	}
	delete(store, id)
	if time.Now().After(e.expiresAt) {
		return false
	}
	// Case-insensitive comparison
	if len(answer) != len(e.answer) {
		return false
	}
	for i := 0; i < len(answer); i++ {
		a, b := answer[i], e.answer[i]
		if a >= 'a' && a <= 'z' {
			a -= 32
		}
		if b >= 'a' && b <= 'z' {
			b -= 32
		}
		if a != b {
			return false
		}
	}
	return true
}

// renderCaptcha draws the text with interference stripes onto an image.
func renderCaptcha(text string, width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Background: light random color
	bgR := uint8(230 + mrand.Intn(25))
	bgG := uint8(230 + mrand.Intn(25))
	bgB := uint8(230 + mrand.Intn(25))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{bgR, bgG, bgB, 255})
		}
	}

	// Draw interference stripes (lines)
	for i := 0; i < 6; i++ {
		lineColor := color.RGBA{
			uint8(mrand.Intn(200)),
			uint8(mrand.Intn(200)),
			uint8(mrand.Intn(200)),
			255,
		}
		x1 := mrand.Intn(width)
		y1 := mrand.Intn(height)
		x2 := mrand.Intn(width)
		y2 := mrand.Intn(height)
		drawLine(img, x1, y1, x2, y2, lineColor, 2)
	}

	// Draw noise dots
	for i := 0; i < 100; i++ {
		x := mrand.Intn(width)
		y := mrand.Intn(height)
		c := color.RGBA{
			uint8(mrand.Intn(255)),
			uint8(mrand.Intn(255)),
			uint8(mrand.Intn(255)),
			255,
		}
		img.Set(x, y, c)
	}

	// Draw each character
	charWidth := width / (len(text) + 1)
	for i, ch := range text {
		x := charWidth/2 + i*charWidth + mrand.Intn(6) - 3
		y := height/2 + mrand.Intn(10) - 5
		charColor := color.RGBA{
			uint8(mrand.Intn(100)),
			uint8(mrand.Intn(100)),
			uint8(mrand.Intn(100)),
			255,
		}
		drawChar(img, x, y, byte(ch), charColor)
	}

	// Draw more interference stripes on top
	for i := 0; i < 3; i++ {
		lineColor := color.RGBA{
			uint8(100 + mrand.Intn(155)),
			uint8(100 + mrand.Intn(155)),
			uint8(100 + mrand.Intn(155)),
			180,
		}
		x1 := 0
		y1 := mrand.Intn(height)
		x2 := width
		y2 := mrand.Intn(height)
		drawLine(img, x1, y1, x2, y2, lineColor, 1)
	}

	return img
}

// drawLine draws a line with given thickness using Bresenham-like approach.
func drawLine(img *image.RGBA, x1, y1, x2, y2 int, c color.RGBA, thickness int) {
	dx := x2 - x1
	dy := y2 - y1
	steps := abs(dx)
	if abs(dy) > steps {
		steps = abs(dy)
	}
	if steps == 0 {
		return
	}
	xInc := float64(dx) / float64(steps)
	yInc := float64(dy) / float64(steps)
	x := float64(x1)
	y := float64(y1)
	half := thickness / 2
	for i := 0; i <= steps; i++ {
		for t := -half; t <= half; t++ {
			img.Set(int(x), int(y)+t, c)
			img.Set(int(x)+t, int(y), c)
		}
		x += xInc
		y += yInc
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// generateCaptchaID creates a cryptographically random captcha ID.
func generateCaptchaID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails (should never happen)
		return fmt.Sprintf("cap_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("cap_%x", b)
}

// drawChar renders a single character using a simple bitmap font.
func drawChar(img *image.RGBA, cx, cy int, ch byte, c color.RGBA) {
	glyph := getGlyph(ch)
	if glyph == nil {
		return
	}
	// Each glyph is 8 rows of 6-bit wide patterns
	startX := cx - 3
	startY := cy - 7
	// Apply slight rotation via skew
	skew := float64(mrand.Intn(5)-2) * 0.15
	for row := 0; row < len(glyph); row++ {
		for col := 0; col < 6; col++ {
			if glyph[row]&(1<<(5-col)) != 0 {
				px := startX + col + int(math.Round(float64(row)*skew))
				py := startY + row
				// Draw 2x2 for boldness
				img.Set(px, py, c)
				img.Set(px+1, py, c)
				img.Set(px, py+1, c)
				img.Set(px+1, py+1, c)
			}
		}
	}
}

// getGlyph returns a simple 8-row bitmap for the given character.
func getGlyph(ch byte) []byte {
	// Simple 6-wide x 8-tall bitmap font for digits and uppercase letters
	glyphs := map[byte][]byte{
		'2': {0x3C, 0x42, 0x02, 0x0C, 0x10, 0x20, 0x7E, 0x00},
		'3': {0x3C, 0x42, 0x02, 0x1C, 0x02, 0x42, 0x3C, 0x00},
		'4': {0x04, 0x0C, 0x14, 0x24, 0x7E, 0x04, 0x04, 0x00},
		'5': {0x7E, 0x40, 0x7C, 0x02, 0x02, 0x42, 0x3C, 0x00},
		'6': {0x1C, 0x20, 0x40, 0x7C, 0x42, 0x42, 0x3C, 0x00},
		'7': {0x7E, 0x02, 0x04, 0x08, 0x10, 0x10, 0x10, 0x00},
		'8': {0x3C, 0x42, 0x42, 0x3C, 0x42, 0x42, 0x3C, 0x00},
		'9': {0x3C, 0x42, 0x42, 0x3E, 0x02, 0x04, 0x38, 0x00},
		'A': {0x18, 0x24, 0x42, 0x7E, 0x42, 0x42, 0x42, 0x00},
		'B': {0x7C, 0x42, 0x42, 0x7C, 0x42, 0x42, 0x7C, 0x00},
		'C': {0x3C, 0x42, 0x40, 0x40, 0x40, 0x42, 0x3C, 0x00},
		'D': {0x78, 0x44, 0x42, 0x42, 0x42, 0x44, 0x78, 0x00},
		'E': {0x7E, 0x40, 0x40, 0x7C, 0x40, 0x40, 0x7E, 0x00},
		'F': {0x7E, 0x40, 0x40, 0x7C, 0x40, 0x40, 0x40, 0x00},
		'G': {0x3C, 0x42, 0x40, 0x4E, 0x42, 0x42, 0x3C, 0x00},
		'H': {0x42, 0x42, 0x42, 0x7E, 0x42, 0x42, 0x42, 0x00},
		'J': {0x1E, 0x04, 0x04, 0x04, 0x04, 0x44, 0x38, 0x00},
		'K': {0x42, 0x44, 0x48, 0x70, 0x48, 0x44, 0x42, 0x00},
		'L': {0x40, 0x40, 0x40, 0x40, 0x40, 0x40, 0x7E, 0x00},
		'M': {0x42, 0x66, 0x5A, 0x42, 0x42, 0x42, 0x42, 0x00},
		'N': {0x42, 0x62, 0x52, 0x4A, 0x46, 0x42, 0x42, 0x00},
		'P': {0x7C, 0x42, 0x42, 0x7C, 0x40, 0x40, 0x40, 0x00},
		'Q': {0x3C, 0x42, 0x42, 0x42, 0x4A, 0x44, 0x3A, 0x00},
		'R': {0x7C, 0x42, 0x42, 0x7C, 0x48, 0x44, 0x42, 0x00},
		'S': {0x3C, 0x42, 0x40, 0x3C, 0x02, 0x42, 0x3C, 0x00},
		'T': {0x7E, 0x18, 0x18, 0x18, 0x18, 0x18, 0x18, 0x00},
		'U': {0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x3C, 0x00},
		'V': {0x42, 0x42, 0x42, 0x42, 0x24, 0x24, 0x18, 0x00},
		'W': {0x42, 0x42, 0x42, 0x42, 0x5A, 0x66, 0x42, 0x00},
		'X': {0x42, 0x24, 0x18, 0x18, 0x24, 0x42, 0x42, 0x00},
		'Y': {0x42, 0x42, 0x24, 0x18, 0x18, 0x18, 0x18, 0x00},
		'Z': {0x7E, 0x04, 0x08, 0x10, 0x20, 0x40, 0x7E, 0x00},
	}
	return glyphs[ch]
}
