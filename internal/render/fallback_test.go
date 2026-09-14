// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"
)

func TestFallbackOverlayAddsStatusBand(t *testing.T) {
	base := func(time.Duration, uint64) ([]byte, error) {
		img := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	data, err := FallbackOverlay(base, "영상 출력 오류 · 정적 화면")(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds(); got != image.Rect(0, 0, landscapeWidth, landscapeHeight) {
		t.Fatalf("bounds = %v", got)
	}
	if r, g, b, a := img.At(40, 410).RGBA(); r == 0 && g == 0 && b == 0 && a == 0 {
		t.Fatal("status band was not rendered")
	}
}

func TestFallbackOverlayRejectsNilBase(t *testing.T) {
	if _, err := FallbackOverlay(nil, "message")(0, 0); err == nil {
		t.Fatal("nil base unexpectedly succeeded")
	}
}
