// SPDX-License-Identifier: GPL-3.0-or-later
//
// TURZX protocol reference: https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later.
// PNG encoding uses Go's standard library; no upstream encoder code is copied.

package turzx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

const (
	nativeWidth  = 462
	nativeHeight = 1920
)

// rgbaPNG disables image/png's opaque-to-RGB optimization without changing pixels.
type rgbaPNG struct{ image.Image }

func (rgbaPNG) ColorModel() color.Model { return color.NRGBAModel }
func (rgbaPNG) Opaque() bool            { return false }

// EncodePNG encodes a native-size image as 8-bit RGBA for the panel.
// The caller must rotate landscape images before encoding. Higher precision
// colors are converted to 8 bits; NRGBA pixels retain their straight alpha.
func EncodePNG(img image.Image) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("encode PNG: nil image")
	}
	if b := img.Bounds(); b.Dx() != nativeWidth || b.Dy() != nativeHeight {
		return nil, fmt.Errorf("encode PNG: dimensions must be %dx%d", nativeWidth, nativeHeight)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, rgbaPNG{img}); err != nil {
		return nil, fmt.Errorf("encode PNG: %w", err)
	}
	if err := ValidatePNG(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ValidatePNG checks the panel's size, 8-bit RGBA format, payload limit and
// PNG integrity. Dimensions are checked before decoding to bound allocation.
func ValidatePNG(data []byte) error {
	if len(data) > MaxPayload {
		return fmt.Errorf("validate PNG: payload exceeds %d bytes", MaxPayload)
	}
	if len(data) < 33 || string(data[:8]) != "\x89PNG\r\n\x1a\n" ||
		binary.BigEndian.Uint32(data[8:12]) != 13 || string(data[12:16]) != "IHDR" {
		return fmt.Errorf("validate PNG: missing PNG IHDR")
	}
	if binary.BigEndian.Uint32(data[16:20]) != nativeWidth || binary.BigEndian.Uint32(data[20:24]) != nativeHeight {
		return fmt.Errorf("validate PNG: dimensions must be %dx%d", nativeWidth, nativeHeight)
	}
	if data[24] != 8 || data[25] != 6 {
		return fmt.Errorf("validate PNG: requires 8-bit RGBA (color type 6)")
	}
	reader := bytes.NewReader(data)
	if _, err := png.Decode(reader); err != nil {
		return fmt.Errorf("validate PNG: %w", err)
	}
	if reader.Len() != 0 {
		return fmt.Errorf("validate PNG: trailing data")
	}
	return nil
}
