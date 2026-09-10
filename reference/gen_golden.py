#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# turzx-control — turing-smart-screen-python(GPL-3.0-or-later)을 레퍼런스로 사용한다.
"""업스트림 turing-smart-screen-python 구현으로 golden 패킷을 생성한다.

Go 포팅이 바이트 단위로 맞는지 USB 장치 없이 검증하기 위한 기준값이다.
타임스탬프 필드(4:8)는 0으로 고정한다 — 그 4바이트만 시간에 따라 변하고 나머지는 결정적이다.

사용법:  python3 gen_golden.py <turing-smart-screen-python 체크아웃 경로>
출력:    ../testdata/golden.json
"""
import sys, json, pathlib

repo = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "tss").resolve()
sys.path.insert(0, str(repo))
import library.lcd.lcd_comm_turing_usb as T

def header(cmd_id):
    p = T.build_command_packet_header(cmd_id)
    p[4:8] = b"\x00\x00\x00\x00"          # 타임스탬프 고정
    return p

cases = {}

# 명령 10: sync
cases["sync"] = header(10)

# 명령 14: brightness (raw 0-102)
for lvl in (0, 50, 102):
    p = header(14); p[8] = lvl
    cases[f"brightness_{lvl}"] = p

# 명령 102: PNG 업로드 헤더 (페이로드 크기가 [8:12]에 big-endian)
for size in (1, 5811, 33131, 1048576):
    p = header(T.CMD_UPLOAD_PNG)
    p[8], p[9], p[10], p[11] = (size >> 24) & 255, (size >> 16) & 255, (size >> 8) & 255, size & 255
    cases[f"upload_png_{size}"] = p

# 명령 101: JPEG 업로드 헤더
for size in (30310,):
    p = header(T.CMD_UPLOAD_JPEG)
    p[8], p[9], p[10], p[11] = (size >> 24) & 255, (size >> 16) & 255, (size >> 8) & 255, size & 255
    cases[f"upload_jpeg_{size}"] = p

out = {}
for name, plain in cases.items():
    enc = T.encrypt_command_packet(plain)
    assert len(enc) == 512, len(enc)
    out[name] = {"plain": bytes(plain).hex(), "encrypted": bytes(enc).hex()}

dest = pathlib.Path(__file__).parent.parent / "testdata" / "golden.json"
dest.write_text(json.dumps(out, indent=2, sort_keys=True) + "\n")
print(f"{len(out)}개 케이스 → {dest}")
print("DES key/IV = slv3tuzx, 트레일러 [510]=161 [511]=26")
