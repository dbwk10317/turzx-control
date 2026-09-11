// SPDX-License-Identifier: GPL-3.0-or-later
//
// turzx-control - TURZX/Turing USB 스마트 스크린 패널용 크로스플랫폼 데몬
//
// 이 파일의 프로토콜 구현은 turing-smart-screen-python의 구현을 참조해 Go로 옮긴 것이다.
// https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package turzx

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// golden.json은 업스트림 Python 구현(turing-smart-screen-python 3.10.0)이 만든 실제 패킷이다.
// 타임스탬프 필드(4:8)는 0으로 고정되어 있다 — 그 4바이트만 시간에 따라 변하고 나머지는 결정적이다.
//
// 재생성하려면 업스트림을 3.10.0 태그로 받아
// library.lcd.lcd_comm_turing_usb 의 build_command_packet_header() 로 평문을 만들고
// 타임스탬프를 0으로 덮은 뒤 encrypt_command_packet() 을 통과시켜 hex로 덤프한다.
type goldenCase struct {
	Plain     string `json:"plain"`
	Encrypted string `json:"encrypted"`
}

// build는 케이스 이름으로부터 평문 패킷을 재구성한다.
func build(t *testing.T, name string) []byte {
	t.Helper()
	switch {
	case name == "sync":
		return BuildHeader(CmdSync, 0)
	case strings.HasPrefix(name, "brightness_"):
		lvl, err := strconv.Atoi(strings.TrimPrefix(name, "brightness_"))
		if err != nil {
			t.Fatalf("케이스 이름 파싱 실패 %q: %v", name, err)
		}
		p := BuildHeader(CmdBrightness, 0)
		p[8] = byte(lvl)
		return p
	case strings.HasPrefix(name, "upload_png_"), strings.HasPrefix(name, "upload_jpeg_"):
		cmd := byte(CmdUploadPNG)
		prefix := "upload_png_"
		if strings.HasPrefix(name, "upload_jpeg_") {
			cmd, prefix = CmdUploadJPEG, "upload_jpeg_"
		}
		size, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		if err != nil {
			t.Fatalf("케이스 이름 파싱 실패 %q: %v", name, err)
		}
		p := BuildHeader(cmd, 0)
		SetPayloadSize(p, uint32(size))
		return p
	}
	t.Fatalf("알 수 없는 케이스: %s", name)
	return nil
}

func TestGoldenPackets(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatalf("golden 읽기 실패: %v", err)
	}
	var cases map[string]goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("golden 파싱 실패: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("golden 케이스가 비어 있다")
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			plain := build(t, name)
			if got := hex.EncodeToString(plain); got != want.Plain {
				t.Errorf("평문 불일치\n got: %s\nwant: %s", got, want.Plain)
			}
			enc, err := EncryptPacket(plain)
			if err != nil {
				t.Fatalf("암호화 실패: %v", err)
			}
			if len(enc) != packetLen {
				t.Fatalf("프레임 길이 %d, 기대 %d", len(enc), packetLen)
			}
			if got := hex.EncodeToString(enc); got != want.Encrypted {
				t.Errorf("암호문 불일치\n got: %s\nwant: %s", got, want.Encrypted)
			}
		})
	}
}

// 페이로드 크기 필드만 빅엔디언이라는 점은 틀리기 쉬우니 따로 고정한다.
func TestPayloadSizeIsBigEndian(t *testing.T) {
	p := BuildHeader(CmdUploadPNG, 0)
	SetPayloadSize(p, 0x01020304)
	for i, want := range []byte{0x01, 0x02, 0x03, 0x04} {
		if p[8+i] != want {
			t.Fatalf("[%d] = %#x, 기대 %#x", 8+i, p[8+i], want)
		}
	}
}

func TestImageCommandLayout(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	full, err := ImageCommand(CmdUploadPNG, 0, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != packetLen+len(payload) {
		t.Fatalf("길이 %d, 기대 %d", len(full), packetLen+len(payload))
	}
	if got := full[packetLen:]; string(got) != string(payload) {
		t.Fatalf("페이로드가 헤더 뒤에 그대로 오지 않았다: %x", got)
	}
	if full[510] != 161 || full[511] != 26 {
		t.Fatalf("트레일러 불일치: %#x %#x", full[510], full[511])
	}
}

func TestPayloadTooLarge(t *testing.T) {
	if _, err := ImageCommand(CmdUploadPNG, 0, make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("페이로드 초과인데 에러가 없다")
	}
}

func TestVideoChunkCommandLayout(t *testing.T) {
	payload := []byte{0, 0, 0, 1, 0x67}
	full, err := VideoChunkCommand(0, payload, true)
	if err != nil {
		t.Fatal(err)
	}
	want := BuildHeader(CmdVideoChunk, 0)
	SetPayloadSize(want, uint32(len(payload)))
	want[12] = 1
	want, err = EncryptPacket(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := full[:packetLen]; string(got) != string(want) {
		t.Fatalf("video header mismatch: %x", got)
	}
	if got := full[packetLen:]; string(got) != string(payload) {
		t.Fatalf("video payload mismatch: %x", got)
	}
	for _, payload := range [][]byte{nil, make([]byte, MaxPayload+1)} {
		if _, err := VideoChunkCommand(0, payload, false); err == nil {
			t.Fatalf("accepted H264 chunk length %d", len(payload))
		}
	}
}
