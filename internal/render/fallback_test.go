// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"
)

func renderNotice(t *testing.T, notice string) image.Image {
	t.Helper()
	data, err := AzureOverlay(func() Dashboard { return Dashboard{At: time.Unix(0, 0), Notice: notice} })(0, 0)
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
	return img
}

func TestNoticeBandIsDrawnOnlyWhenSet(t *testing.T) {
	// The band must change the finished dashboard where it sits and leave the
	// rest of it alone; an empty notice must change nothing at all.
	with := renderNotice(t, "영상 출력 오류 · 정적 화면")
	without := renderNotice(t, "")
	if with.At(40, 410) == without.At(40, 410) {
		t.Fatal("notice band was not rendered")
	}
	if with.At(40, 100) != without.At(40, 100) {
		t.Fatal("notice band bled outside its own rows")
	}
}

func TestOverlayRejectsNilSnapshot(t *testing.T) {
	if _, err := AzureOverlay(nil)(0, 0); err == nil {
		t.Fatal("nil snapshot unexpectedly succeeded")
	}
	if _, err := HalloweenOverlay(nil)(0, 0); err == nil {
		t.Fatal("nil snapshot unexpectedly succeeded")
	}
}
