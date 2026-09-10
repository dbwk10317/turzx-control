#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# turzx-control — turing-smart-screen-python(GPL-3.0-or-later)을 레퍼런스로 사용한다.
"""배경 종류 × 인코딩별 프레임 크기를 잰다. 프레임 크기가 곧 fps 예산이라 테마 디자인 제약이 된다.
USB 장치가 없어도 돌아간다.  사용법: python3 measure_encoding.py"""
import math
from io import BytesIO
from PIL import Image, ImageDraw

W, H = 1920, 462

def bg(kind, t=1.7):
    img = Image.new("RGB", (W, H), (8, 10, 16)); d = ImageDraw.Draw(img)
    if kind == "그라디언트 물결":
        for x in range(0, W, 6):
            y = H/2 + math.sin(x/180+t*2.2)*130 + math.sin(x/70-t*1.4)*45
            c = int(90+90*math.sin(x/300+t)); d.line([(x, y), (x, H)], fill=(c//4, c, 150), width=6)
    elif kind == "단색 + 도형 몇 개":
        d.rectangle([0, 300, W, H], fill=(20, 60, 90))
        for i in range(6):
            d.ellipse([i*320-40, 260, i*320+180, 420], fill=(30, 90, 130))
    elif kind == "풀 그라디언트(부드러움)":
        for x in range(W):
            c = int(127+120*math.sin(x/240+t)); d.line([(x, 0), (x, H)], fill=(c//3, c//2, c))
    return img

def size(img, mode):
    b = BytesIO()
    if mode == "PNG24": img.save(b, "PNG")
    elif mode == "PNG-팔레트64": img.convert("P", palette=Image.ADAPTIVE, colors=64).save(b, "PNG")
    elif mode == "PNG-팔레트16": img.convert("P", palette=Image.ADAPTIVE, colors=16).save(b, "PNG")
    elif mode == "JPEG q80": img.save(b, "JPEG", quality=80)
    return len(b.getvalue())/1024

if __name__ == "__main__":
    modes = ["PNG24", "PNG-팔레트64", "PNG-팔레트16", "JPEG q80"]
    print(f"{'배경 종류':<22}" + "".join(f"{m:>16}" for m in modes))
    for k in ("단색 + 도형 몇 개", "그라디언트 물결", "풀 그라디언트(부드러움)"):
        img = bg(k)
        print(f"{k:<22}" + "".join(f"{size(img, m):13.1f}KB" for m in modes))
