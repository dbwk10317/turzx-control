// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"image"
	"image/color"
	"image/draw"
)

// drawNotice puts a diagnostic line over the finished dashboard. Nothing is
// drawn for an empty notice, which is the normal case.
func drawNotice(img draw.Image, notice string) {
	if notice == "" {
		return
	}
	draw.Draw(img, image.Rect(34, 394, 1886, 448), image.NewUniform(color.NRGBA{R: 12, G: 9, B: 24, A: 220}), image.Point{}, draw.Over)
	text(img, true, 58, 429, notice, 22, halloweenGold)
}
