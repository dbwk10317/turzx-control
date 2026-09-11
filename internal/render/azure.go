// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// AzurePreviewOverlay renders the design validation sample overlay for the
// azure-ribbon background. Values are deliberately preview data, independent
// of the metric package and live usage sources. It embeds Pretendard v1.3.9
// from https://github.com/orioncactus/pretendard under the SIL OFL 1.1 in
// fonts/OFL.txt so the preview has no system-font dependency.
func AzurePreviewOverlay(elapsed time.Duration, counter uint64) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
	glass := color.NRGBA{R: 3, G: 19, B: 27, A: 174}
	glassEdge := color.NRGBA{R: 96, G: 165, B: 250, A: 64}
	cyan := color.NRGBA{R: 34, G: 211, B: 238, A: 255}
	blue := color.NRGBA{R: 96, G: 165, B: 250, A: 255}
	ink := color.NRGBA{R: 230, G: 247, B: 255, A: 255}
	muted := color.NRGBA{R: 145, G: 169, B: 184, A: 255}

	drawRounded(img, image.Rect(28, 28, 1892, 434), 24, glass)
	drawRounded(img, image.Rect(28, 28, 1892, 434), 24, glassEdge)
	// Restore the broad translucent rail over its hairline edge.
	drawRounded(img, image.Rect(30, 30, 1890, 432), 22, glass)
	draw.Draw(img, image.Rect(430, 66, 431, 397), image.NewUniform(color.NRGBA{R: 96, G: 165, B: 250, A: 62}), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(1230, 66, 1231, 397), image.NewUniform(color.NRGBA{R: 96, G: 165, B: 250, A: 62}), image.Point{}, draw.Src)

	text(img, true, 64, 134, "현재 시각", 18, muted)
	clock := time.Date(2026, 9, 11, 8, 42, 36, 0, time.Local).Add(elapsed).Format("15:04:05")
	text(img, true, 58, 236, clock, 80, ink)
	text(img, false, 64, 284, "2026-09-11", 26, muted)
	drawRounded(img, image.Rect(62, 312, 322, 345), 16, color.NRGBA{R: 34, G: 211, B: 238, A: 30})
	drawRounded(img, image.Rect(74, 324, 82, 332), 4, cyan)
	text(img, true, 94, 337, fmt.Sprintf("실시간  %s  #%06d", formatElapsed(elapsed), counter), 16, cyan)
	text(img, false, 1680, 72, fmt.Sprintf("미리보기 데이터 #%06d", counter), 15, muted)

	text(img, true, 478, 104, "AI 에이전트", 18, muted)
	text(img, true, 478, 146, "CODEX", 30, cyan)
	text(img, true, 838, 146, "CLAUDE", 30, blue)
	drawQuota(img, 478, 186, "5시간", "74%", "초기화까지 2시간 18분", 0.74, cyan)
	drawQuota(img, 478, 310, "주간", "61%", "초기화까지 3일 6시간", 0.61, cyan)
	drawQuota(img, 838, 186, "5시간", "88%", "초기화까지 1시간 42분", 0.88, blue)
	drawQuota(img, 838, 310, "주간", "49%", "초기화까지 4일 11시간", 0.49, blue)

	text(img, true, 1278, 92, "하드웨어 모니터", 18, muted)
	drawMetric(img, 1278, 130, "CPU", "34%", "52°C", 0.34, cyan)
	drawMetric(img, 1278, 230, "GPU", "67%", "61°C", 0.67, blue)
	drawMetric(img, 1278, 330, "메모리", "58%", "48°C", 0.58, cyan)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

var (
	//go:embed fonts/Pretendard-Regular.otf
	pretendardRegular []byte
	//go:embed fonts/Pretendard-SemiBold.otf
	pretendardSemiBold []byte

	azureFontOnce sync.Once
	azureRegular  *sfnt.Font
	azureBold     *sfnt.Font
	azureFacesMu  sync.Mutex
	azureFaces    = make(map[string]font.Face)
)

func text(img draw.Image, bold bool, x, y int, value string, size float64, c color.Color) {
	azureFontOnce.Do(func() {
		azureRegular, _ = opentype.Parse(pretendardRegular)
		azureBold, _ = opentype.Parse(pretendardSemiBold)
	})
	key := fmt.Sprintf("%t/%.1f", bold, size)
	azureFacesMu.Lock()
	face := azureFaces[key]
	if face == nil {
		parsed := azureRegular
		if bold {
			parsed = azureBold
		}
		face, _ = opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		azureFaces[key] = face
	}
	defer azureFacesMu.Unlock()
	if face == nil {
		return
	}
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(value)
}

func drawQuota(img draw.Image, x, y int, period, value, reset string, fraction float64, accent color.NRGBA) {
	text(img, false, x, y, period, 18, color.NRGBA{R: 145, G: 169, B: 184, A: 255})
	text(img, true, x, y+45, value, 42, color.NRGBA{R: 230, G: 247, B: 255, A: 255})
	text(img, false, x+112, y+42, reset, 19, color.NRGBA{R: 145, G: 169, B: 184, A: 255})
	drawRail(img, x, y+62, 344, fraction, accent)
}

func drawMetric(img draw.Image, x, y int, label, usage, temp string, fraction float64, accent color.NRGBA) {
	text(img, false, x, y, label, 18, color.NRGBA{R: 145, G: 169, B: 184, A: 255})
	text(img, true, x, y+43, usage, 42, color.NRGBA{R: 230, G: 247, B: 255, A: 255})
	text(img, false, x+125, y+40, temp, 22, accent)
	drawRail(img, x, y+55, 564, fraction, accent)
}

func drawRail(img draw.Image, x, y, width int, fraction float64, accent color.NRGBA) {
	drawRounded(img, image.Rect(x, y, x+width, y+5), 2, color.NRGBA{R: 96, G: 165, B: 250, A: 35})
	for i := 3; i >= 0; i-- {
		alpha := uint8(30 + i*25)
		drawRounded(img, image.Rect(x-i, y-i, x+int(float64(width)*fraction)+i, y+5+i), 3+i, color.NRGBA{R: accent.R, G: accent.G, B: accent.B, A: alpha})
	}
	drawRounded(img, image.Rect(x, y, x+int(float64(width)*fraction), y+5), 2, accent)
}

func drawRounded(img draw.Image, r image.Rectangle, radius int, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dx, dy := 0, 0
			if x < r.Min.X+radius {
				dx = r.Min.X + radius - x
			}
			if x >= r.Max.X-radius {
				dx = x - (r.Max.X - radius - 1)
			}
			if y < r.Min.Y+radius {
				dy = r.Min.Y + radius - y
			}
			if y >= r.Max.Y-radius {
				dy = y - (r.Max.Y - radius - 1)
			}
			if dx*dx+dy*dy <= radius*radius {
				img.Set(x, y, c)
			}
		}
	}
}
