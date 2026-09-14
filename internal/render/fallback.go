// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"time"
)

// FallbackOverlay adds a compact diagnostic label to a theme overlay used by
// the static PNG path. The base overlay remains responsible for all dashboard
// content and layout.
func FallbackOverlay(base Overlay, message string) Overlay {
	return func(elapsed time.Duration, counter uint64) ([]byte, error) {
		if base == nil {
			return nil, fmt.Errorf("fallback overlay: nil base overlay")
		}
		data, err := base(elapsed, counter)
		if err != nil {
			return nil, fmt.Errorf("fallback overlay base: %w", err)
		}
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("fallback overlay decode: %w", err)
		}
		img := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
		draw.Draw(img, img.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
		draw.Draw(img, image.Rect(34, 394, 1886, 448), image.NewUniform(color.NRGBA{R: 12, G: 9, B: 24, A: 220}), image.Point{}, draw.Over)
		text(img, true, 58, 429, message, 22, halloweenGold)
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			return nil, fmt.Errorf("fallback overlay encode: %w", err)
		}
		return out.Bytes(), nil
	}
}
