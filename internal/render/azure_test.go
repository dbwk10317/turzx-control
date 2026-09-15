// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"image"
	"image/color"
	"testing"
)

// The account must never spill into the next column: the panel's columns are
// fixed and an overlapping address is unreadable.
func TestProviderAccountFitsItsColumn(t *testing.T) {
	long := "an-extremely-long-account-name@a-very-long-organisation.example.com"
	if width := textWidth(false, long, 28); width <= azureColumn {
		t.Fatalf("test address is too short to exercise truncation: %d px", width)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 1920, 462))
	drawProviderLabel(img, 478, 146, "CODEX", long, 30, 28, azureColumn, color.White, color.White)
	// Anything drawn at or past the next column's origin would overlap CLAUDE.
	for y := 100; y < 160; y++ {
		for x := 478 + azureColumn; x < 1920; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
				t.Fatalf("drew into the next column at (%d, %d)", x, y)
			}
		}
	}
}
