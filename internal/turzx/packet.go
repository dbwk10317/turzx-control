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

// Package turzx는 TURZX/Turing USB 스마트 스크린 패널의 벤더 프로토콜을 구현한다.
//
// 프로토콜(mathoudebine/turing-smart-screen-python 3.10.0에서 확인):
//
//  1. 500바이트 평문 명령 패킷: [0]=명령ID, [2]=0x1A, [3]=0x6D,
//     [4:8]=자정 이후 경과 밀리초(리틀엔디언), 나머지는 명령별.
//  2. DES-CBC로 암호화한다. 키와 IV가 모두 "slv3tuzx"이고 패딩은 0바이트다
//     (PKCS#7이 아니다). 500바이트는 504바이트로 패딩된다.
//  3. 512바이트 프레임에 담고 [510]=161, [511]=26 트레일러를 붙인다.
//  4. USB 인터페이스 0의 벌크 OUT으로 보낸다. 이미지 명령은 암호화된
//     512바이트 헤더 뒤에 인코딩된 이미지 바이트를 그대로 이어 붙인다.
//
// # 픽셀 포맷 — 반드시 RGBA
//
// PNG 페이로드는 RGBA로 인코딩해야 한다. 다른 포맷은 전송이 성공하고 장치가 정상 응답을
// 돌려주지만 화면이 잘못 그려지며, 오류로 드러나지 않는다:
//
//   - RGB: 이미지가 세로 1/4 크기로 축소되어 4번 반복되고, 배경이 갱신되지 않아
//     이전 화면 위에 밝은 픽셀만 겹쳐 그려진다.
//   - 팔레트(P 모드): 완전히 무시된다. 화면이 전혀 갱신되지 않는다.
//
// 인코딩 결과는 8bit RGBA(IHDR color type 6)를 유지해야 한다.
// 이 형식을 유지하면서 색상 수를 줄이거나 압축 설정을 조정하는 최적화는 가능하다.
//
// 가로 방향으로 쓰려면 1920x462로 그린 뒤 시계 방향 90도 회전해 462x1920으로 보낸다.
// 이는 Pillow ROTATE_270과 같은 방향이다.
package turzx

import (
	"crypto/cipher"
	"crypto/des"
	"encoding/binary"
	"fmt"
	"time"
)

// 벤더 명령 ID.
const (
	CmdSync           = 10
	CmdRestart        = 11
	CmdVideoInit13    = 13
	CmdBrightness     = 14
	CmdFrameRate      = 15
	CmdVideoChunkSize = 17
	CmdVideoInit41    = 41
	CmdUploadJPEG     = 101
	CmdUploadPNG      = 102
	CmdVideoInit111   = 111
	CmdVideoInit112   = 112
	CmdVideoChunk     = 121
	CmdVideoStatus    = 122
	CmdVideoStop      = 123
)

const (
	headerLen  = 500 // 평문 명령 패킷 길이
	packetLen  = 512 // 전송되는 프레임 길이
	MaxPayload = 1 << 20
)

// desKey는 키이자 IV다. 펌웨어에 하드코딩되어 있다.
var desKey = []byte("slv3tuzx")

// BuildHeader는 평문 명령 패킷을 만든다. tsMillis는 자정 이후 경과 밀리초다.
func BuildHeader(cmd byte, tsMillis uint32) []byte {
	p := make([]byte, headerLen)
	p[0] = cmd
	p[2] = 0x1A
	p[3] = 0x6D
	binary.LittleEndian.PutUint32(p[4:8], tsMillis)
	return p
}

// MillisSinceMidnight는 장치가 기대하는 타임스탬프 값을 만든다.
func MillisSinceMidnight(t time.Time) uint32 {
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return uint32(t.Sub(midnight).Milliseconds())
}

// EncryptPacket은 평문 패킷을 전송 가능한 512바이트 프레임으로 만든다.
func EncryptPacket(plain []byte) ([]byte, error) {
	if len(plain) > headerLen {
		return nil, fmt.Errorf("평문 패킷이 %d바이트를 넘는다: %d", headerLen, len(plain))
	}
	block, err := des.NewCipher(desKey)
	if err != nil {
		return nil, err
	}
	padded := make([]byte, (len(plain)+7)/8*8) // 0바이트 패딩
	copy(padded, plain)

	enc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, desKey).CryptBlocks(enc, padded)

	out := make([]byte, packetLen)
	copy(out, enc)
	out[510] = 161
	out[511] = 26
	return out, nil
}

// SetPayloadSize는 이미지 업로드 명령의 페이로드 크기를 [8:12]에 빅엔디언으로 넣는다.
// 헤더의 나머지 필드가 리틀엔디언인 것과 달리 이 필드만 빅엔디언이다.
func SetPayloadSize(header []byte, size uint32) {
	binary.BigEndian.PutUint32(header[8:12], size)
}

// ImageCommand는 이미지 한 장을 보낼 전체 바이트열을 만든다.
func ImageCommand(cmd byte, tsMillis uint32, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("페이로드가 %d바이트를 넘는다: %d", MaxPayload, len(payload))
	}
	h := BuildHeader(cmd, tsMillis)
	SetPayloadSize(h, uint32(len(payload)))
	enc, err := EncryptPacket(h)
	if err != nil {
		return nil, err
	}
	return append(enc, payload...), nil
}

// VideoChunkCommand builds command 121 and appends one H264 Annex B chunk.
// The final flag is only valid for a finite stream whose end is known.
func VideoChunkCommand(tsMillis uint32, payload []byte, final bool) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("H264 chunk is empty")
	}
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("H264 chunk exceeds %d bytes: %d", MaxPayload, len(payload))
	}
	h := BuildHeader(CmdVideoChunk, tsMillis)
	SetPayloadSize(h, uint32(len(payload)))
	if final {
		h[12] = 1
	}
	enc, err := EncryptPacket(h)
	if err != nil {
		return nil, err
	}
	return append(enc, payload...), nil
}
