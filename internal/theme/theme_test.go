// SPDX-License-Identifier: GPL-3.0-or-later

package theme

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validFixture(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "bg.mp4"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"id":"x","name":"X","canvas":{"x":0,"y":0,"width":1920,"height":462},"background":{"path":"bg.mp4","fit":"cover"},"font":{"family":"Pretendard","regular":"pretendard.regular","semibold":"pretendard.semibold"},"display_refresh_ms":1000,"palette":{"ink":"#FFFFFF"},"regions":{"clock":{"x":0,"y":0,"width":100,"height":100}},"elements":[{"id":"e","region":"clock","kind":"text","text":"ok","bounds":{"x":1,"y":20,"width":10,"height":10},"color":"ink"}]}`
	p := filepath.Join(d, "theme.json")
	if err := os.WriteFile(p, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	if _, err := Load(validFixture(t)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAzureRibbonManifest(t *testing.T) {
	source := filepath.Join("..", "..", "assets", "backgrounds", "azure-ribbon.theme.json")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	p := filepath.Join(d, "azure-ribbon.theme.json")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "azure-ribbon.mp4"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "azure-ribbon" || m.DisplayRefresh != 1000 || len(m.Elements) == 0 {
		t.Fatalf("unexpected azure-ribbon contract: id=%q refresh=%d elements=%d", m.ID, m.DisplayRefresh, len(m.Elements))
	}
}

func TestLoadRejectsDifferentV1Refresh(t *testing.T) {
	p := validFixture(t)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), `"display_refresh_ms":1000`, `"display_refresh_ms":10000`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("different v1 refresh accepted")
	}
}

func TestLoadRejectsUnknownSourceAndField(t *testing.T) {
	p := validFixture(t)
	b, _ := os.ReadFile(p)
	for _, repl := range []string{`"text":"ok"`, `"color":"ink"`} {
		_ = repl
	}
	s := strings.Replace(string(b), `"id":"e"`, `"id":"e","unknown":1`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("unknown field accepted")
	}
	s = strings.Replace(s, `,"unknown":1`, `,"source":"nope"`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("unknown source accepted")
	}
}

func TestLoadRejectsDuplicateKeysAtAnyObjectDepth(t *testing.T) {
	p := validFixture(t)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), `"palette":{"ink":"#FFFFFF"}`, `"palette":{"ink":"#FFFFFF","ink":"#000000"}`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("duplicate nested key not rejected: %v", err)
	}
}

func TestLoadRejectsDuplicateAndOutOfRegion(t *testing.T) {
	p := validFixture(t)
	b, _ := os.ReadFile(p)
	s := string(b)
	s = strings.Replace(s, `"id":"e"`, `"id":"e","source":"clock.time"`, 1)
	s = strings.Replace(s, `"id":"e","source":"clock.time"`, `"id":"e","source":"clock.time"},{"id":"e","region":"clock","kind":"text","text":"x","bounds":{"x":1,"y":20,"width":10,"height":10}}`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("duplicate accepted")
	}
	p = validFixture(t)
	b, _ = os.ReadFile(p)
	s = strings.Replace(string(b), `"width":10,"height":10`, `"width":101,"height":10`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("out of region accepted")
	}
}

func TestLoadRejectsPathEscape(t *testing.T) {
	p := validFixture(t)
	b, _ := os.ReadFile(p)
	s := strings.Replace(string(b), `bg.mp4`, `../bg.mp4`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("path escape accepted")
	}
}

func TestLoadRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	p := validFixture(t)
	d := filepath.Dir(p)
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link.mp4")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	b, _ := os.ReadFile(p)
	s := strings.Replace(string(b), `bg.mp4`, `link.mp4`, 1)
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
