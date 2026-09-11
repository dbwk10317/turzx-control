// SPDX-License-Identifier: GPL-3.0-or-later

// Package theme loads and validates v1 display theme manifests.
package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	CanvasWidth  = 1920
	CanvasHeight = 462
)

type Manifest struct {
	Version        int               `json:"version"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Canvas         Bounds            `json:"canvas"`
	Background     Background        `json:"background"`
	Fonts          Fonts             `json:"font"`
	DisplayRefresh int               `json:"display_refresh_ms"`
	Palette        map[string]string `json:"palette"`
	Regions        map[string]Bounds `json:"regions"`
	Elements       []Element         `json:"elements"`
	ManifestPath   string            `json:"-"`
	BackgroundPath string            `json:"-"`
}

type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}
type Background struct {
	Path string `json:"path"`
	Fit  string `json:"fit"`
}
type Fonts struct {
	Family   string `json:"family"`
	Regular  string `json:"regular"`
	Semibold string `json:"semibold"`
}

// Element.Bounds uses coordinates relative to Element.Region; the region itself
// uses coordinates relative to the canvas. For text, X and Y are the left
// baseline anchor used by the renderer; Width extends right and Height reserves
// space above that baseline. For bars, X and Y are the top-left corner and the
// size extends right and down.
type Element struct {
	ID     string  `json:"id"`
	Region string  `json:"region"`
	Kind   string  `json:"kind"`
	Source string  `json:"source,omitempty"`
	Text   string  `json:"text,omitempty"`
	Bounds Bounds  `json:"bounds"`
	Font   string  `json:"font,omitempty"`
	Size   float64 `json:"size,omitempty"`
	Color  string  `json:"color,omitempty"`
}

var sources = map[string]bool{
	"clock.time": true, "clock.date": true, "status.live": true,
	"codex.five_hour.remaining": true, "codex.five_hour.reset": true, "codex.week.remaining": true, "codex.week.reset": true,
	"claude.five_hour.remaining": true, "claude.five_hour.reset": true, "claude.week.remaining": true, "claude.week.reset": true,
	"cpu.usage": true, "cpu.temperature": true, "gpu.usage": true, "gpu.temperature": true, "ram.usage": true, "ram.temperature": true,
}

// Load reads and strictly validates a manifest. Relative assets are confined to its directory.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("theme %q: open manifest: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("theme %q: read manifest: %w", path, err)
	}
	if err := duplicateKeys(data); err != nil {
		return nil, fmt.Errorf("theme %q: %w", path, err)
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("theme %q: decode manifest: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("theme %q: trailing JSON value", path)
		}
		return nil, fmt.Errorf("theme %q: trailing JSON: %w", path, err)
	}
	if err := validate(&m, path); err != nil {
		return nil, err
	}
	return &m, nil
}

func duplicateKeys(data []byte) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	if err := scanJSONValue(d); err != nil {
		return err
	}
	return nil
}

func scanJSONValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if seen[name] {
					return fmt.Errorf("duplicate JSON key %q", name)
				}
				seen[name] = true
				if err := scanJSONValue(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
		case '[':
			for d.More() {
				if err := scanJSONValue(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
		}
	}
	return err
}

func validate(m *Manifest, path string) error {
	if m.Version != 1 {
		return fmt.Errorf("theme %q: version must be 1", path)
	}
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("theme %q: id and name are required", path)
	}
	if m.Canvas.Width != CanvasWidth || m.Canvas.Height != CanvasHeight || m.Canvas.X != 0 || m.Canvas.Y != 0 {
		return fmt.Errorf("theme %q: canvas must be %dx%d at (0,0)", path, CanvasWidth, CanvasHeight)
	}
	if m.Background.Fit != "cover" {
		return fmt.Errorf("theme %q: background.fit must be cover", path)
	}
	if err := validateBackground(m, path); err != nil {
		return err
	}
	if m.Fonts.Family == "" || m.Fonts.Regular != "pretendard.regular" || m.Fonts.Semibold != "pretendard.semibold" {
		return fmt.Errorf("theme %q: font family and built-in regular/semibold refs are required", path)
	}
	if m.DisplayRefresh != 1000 {
		return fmt.Errorf("theme %q: v1 display refresh must be 1000 milliseconds", path)
	}
	if len(m.Regions) == 0 {
		return fmt.Errorf("theme %q: regions are required", path)
	}
	for name, b := range m.Regions {
		if name != "clock" && name != "ai_agent" && name != "hw_monitor" {
			return fmt.Errorf("theme %q: unknown region %q", path, name)
		}
		if err := within(b, m.Canvas, "region "+name); err != nil {
			return fmt.Errorf("theme %q: %w", path, err)
		}
	}
	if len(m.Palette) == 0 {
		return fmt.Errorf("theme %q: palette is required", path)
	}
	for name, value := range m.Palette {
		if !validColor(value) {
			return fmt.Errorf("theme %q: palette %q has invalid color %q", path, name, value)
		}
	}
	seen := map[string]bool{}
	for i, e := range m.Elements {
		ctx := fmt.Sprintf("element %d", i)
		if e.ID == "" {
			return fmt.Errorf("theme %q: %s id is required", path, ctx)
		}
		if seen[e.ID] {
			return fmt.Errorf("theme %q: duplicate element id %q", path, e.ID)
		}
		seen[e.ID] = true
		if e.Region != "clock" && e.Region != "ai_agent" && e.Region != "hw_monitor" {
			return fmt.Errorf("theme %q: %s has unknown region %q", path, ctx, e.Region)
		}
		if e.Kind != "text" && e.Kind != "bar" {
			return fmt.Errorf("theme %q: %s has unknown kind %q", path, ctx, e.Kind)
		}
		if (e.Source == "") == (e.Text == "") {
			return fmt.Errorf("theme %q: %s must have exactly one source or text", path, ctx)
		}
		if e.Source != "" && !sources[e.Source] {
			return fmt.Errorf("theme %q: %s has unknown source %q", path, ctx, e.Source)
		}
		if e.Font != "" && e.Font != m.Fonts.Regular && e.Font != m.Fonts.Semibold {
			return fmt.Errorf("theme %q: %s has unknown font %q", path, ctx, e.Font)
		}
		if e.Size < 0 {
			return fmt.Errorf("theme %q: %s size must not be negative", path, ctx)
		}
		if e.Color != "" {
			if _, ok := m.Palette[e.Color]; !ok {
				return fmt.Errorf("theme %q: %s references unknown palette %q", path, ctx, e.Color)
			}
		}
		if err := withinElement(e, m.Regions[e.Region], ctx+" bounds"); err != nil {
			return fmt.Errorf("theme %q: %w", path, err)
		}
	}
	return nil
}

func withinElement(e Element, outer Bounds, label string) error {
	if e.Kind != "text" {
		return within(e.Bounds, outer, label)
	}
	b := e.Bounds
	if b.X < 0 || b.Y < 0 || b.Width <= 0 || b.Height <= 0 {
		return fmt.Errorf("%s must have non-negative anchor and positive size", label)
	}
	if b.Y > outer.Height || b.Height > b.Y || b.X > outer.Width || b.Width > outer.Width-b.X {
		return fmt.Errorf("%s is outside its parent", label)
	}
	return nil
}

func within(b, outer Bounds, label string) error {
	if b.X < 0 || b.Y < 0 || b.Width <= 0 || b.Height <= 0 {
		return fmt.Errorf("%s must have non-negative origin and positive size", label)
	}
	if b.X > outer.Width || b.Y > outer.Height || b.Width > outer.Width-b.X || b.Height > outer.Height-b.Y {
		return fmt.Errorf("%s is outside its parent", label)
	}
	return nil
}

func validColor(s string) bool {
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func validateBackground(m *Manifest, manifest string) error {
	if m.Background.Path == "" || filepath.IsAbs(m.Background.Path) {
		return fmt.Errorf("theme %q: background.path must be a relative path", manifest)
	}
	dir, err := filepath.Abs(filepath.Dir(manifest))
	if err != nil {
		return fmt.Errorf("theme %q: resolve manifest directory: %w", manifest, err)
	}
	candidate := filepath.Join(dir, filepath.FromSlash(m.Background.Path))
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("theme %q: resolve manifest directory: %w", manifest, err)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return fmt.Errorf("theme %q: resolve background: %w", manifest, err)
	}
	rel, err := filepath.Rel(resolvedDir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("theme %q: background escapes manifest directory", manifest)
	}
	if strings.ToLower(filepath.Ext(resolved)) != ".mp4" {
		return fmt.Errorf("theme %q: background must be an mp4 file", manifest)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("theme %q: stat background: %w", manifest, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("theme %q: background must be a nonempty regular file", manifest)
	}
	m.ManifestPath, m.BackgroundPath = manifest, resolved
	return nil
}
