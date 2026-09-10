#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# turzx-control — turing-smart-screen-python(GPL-3.0-or-later)을 레퍼런스로 사용한다.
"""배경 종류별 프레임 크기를 잰다. 프레임 크기가 곧 fps 예산이라 테마 디자인 제약이 된다.
USB 장치가 없어도 돌아간다.

장치는 RGBA만 받는다. 팔레트 열은 참고용일 뿐이며 실제로는 쓸 수 없다 — 장치가 무시한다.
"""
import math
from io import BytesIO
from PIL import Image, ImageDraw, ImageFont

W, H = 1920, 462
BIG = ImageFont.truetype("/System/Library/Fonts/Supplemental/Arial Bold.ttf", 96)
SML = ImageFont.truetype("/System/Library/Fonts/Supplemental/Arial.ttf", 34)


def dashboard(kind, t=1.7):
    """실제로 그릴 화면: 수치 4개 + 게이지 + 갱신 시각."""
    img = Image.new("RGBA", (W, H), (10, 12, 20, 255))
    d = ImageDraw.Draw(img)
    if kind == "가로 그라디언트":
        for x in range(W):
            c = int(20 + 40 * math.sin(x / 500 + t))
            d.line([(x, 0), (x, H)], fill=(c // 2, c, c + 40, 255))
    elif kind == "움직이는 물결":
        for x in range(0, W, 6):
            y = H / 2 + math.sin(x / 180 + t * 2.2) * 130 + math.sin(x / 70 - t * 1.4) * 45
            c = int(90 + 90 * math.sin(x / 300 + t))
            d.line([(x, y), (x, H)], fill=(c // 4, c, 150, 255), width=6)
    for i, (lab, val) in enumerate([("CLAUDE 5h", "73%"), ("CODEX 5h", "91%"),
                                    ("CPU", "47C"), ("GPU", "61C")]):
        x = 70 + i * 470
        d.text((x, 60), lab, fill=(150, 160, 180, 255), font=SML)
        d.text((x, 100), val, fill=(255, 255, 255, 255), font=BIG)
        d.rectangle([x, 230, x + 380, 258], fill=(40, 44, 58, 255))
        d.rectangle([x, 230, x + int(380 * (0.4 + 0.15 * i)), 258], fill=(80, 220, 160, 255))
    d.text((70, H - 60), "updated 12:34:56", fill=(110, 118, 135, 255), font=SML)
    return img


def size_kb(img, mode):
    b = BytesIO()
    if mode == "RGBA":
        img.save(b, "PNG", compress_level=9)
    elif mode == "P(장치가 무시)":
        img.convert("P", palette=Image.ADAPTIVE, colors=32).save(b, "PNG", compress_level=9)
    return len(b.getvalue()) / 1024


if __name__ == "__main__":
    modes = ["RGBA", "P(장치가 무시)"]
    print(f"{'대시보드 화면':<18}" + "".join(f"{m:>18}" for m in modes))
    for k in ("단색 배경", "가로 그라디언트", "움직이는 물결"):
        img = dashboard(k)
        print(f"{k:<18}" + "".join(f"{size_kb(img, m):15.1f}KB" for m in modes))
    print("\n실측 fps(RGBA): 38.1KB→14.5fps, 41.9KB→13.5fps (flush 3ms)")
