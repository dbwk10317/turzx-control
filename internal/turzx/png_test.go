// SPDX-License-Identifier: GPL-3.0-or-later
//
// TURZX protocol reference: https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later.

package turzx

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"
)

func TestEncodePNGRoundTrip(t *testing.T) {
	for _, opaque := range []bool{true, false} {
		name := "translucent"
		if opaque {
			name = "opaque"
		}
		t.Run(name, func(t *testing.T) {
			// A nonzero origin also verifies subimage bounds are respected.
			img := image.NewNRGBA(image.Rect(7, 11, 7+nativeWidth, 11+nativeHeight))
			for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
				for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
					a := uint8(x % 256)
					if opaque {
						a = 255
					}
					img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 127, A: a})
				}
			}
			data, err := EncodePNG(img)
			if err != nil {
				t.Fatal(err)
			}
			if data[24] != 8 || data[25] != 6 {
				t.Fatalf("IHDR depth/type = %d/%d", data[24], data[25])
			}
			decoded, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < nativeHeight; y++ {
				for x := 0; x < nativeWidth; x++ {
					want := img.NRGBAAt(x+7, y+11)
					got := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
					if got != want {
						t.Fatalf("pixel %d,%d: got %v, want %v", x, y, got, want)
					}
				}
			}
		})
	}
}

func TestValidatePNGRejectsInvalid(t *testing.T) {
	encode := func(img image.Image) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	bounds := image.Rect(0, 0, nativeWidth, nativeHeight)
	rgb := image.NewRGBA(bounds)
	for i := 3; i < len(rgb.Pix); i += 4 {
		rgb.Pix[i] = 255
	}
	valid, err := EncodePNG(image.NewNRGBA(bounds))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)-1] ^= 1 // Broken IEND checksum after a valid image stream.
	for name, data := range map[string][]byte{
		"empty":       nil,
		"RGB":         encode(rgb),
		"palette":     encode(image.NewPaletted(bounds, color.Palette{color.Black})),
		"16 bit":      encode(image.NewNRGBA64(bounds)),
		"wrong size":  encode(image.NewNRGBA(image.Rect(0, 0, 1920, 462))),
		"header only": valid[:33],
		"truncated":   valid[:len(valid)-1],
		"checksum":    corrupt,
		"trailing":    append(bytes.Clone(valid), 0),
		"oversize":    make([]byte, MaxPayload+1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePNG(data); err == nil {
				t.Fatal("accepted invalid PNG")
			}
		})
	}
}

func TestEncodePNGRejectsInvalid(t *testing.T) {
	for _, img := range []image.Image{nil, image.NewNRGBA(image.Rect(0, 0, 1920, 462))} {
		if _, err := EncodePNG(img); err == nil {
			t.Fatal("accepted invalid image")
		}
	}
	noise := image.NewNRGBA(image.Rect(0, 0, nativeWidth, nativeHeight))
	_, _ = rand.New(rand.NewSource(1)).Read(noise.Pix)
	if _, err := EncodePNG(noise); err == nil {
		t.Fatal("accepted image exceeding payload limit")
	}
}
