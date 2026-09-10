# SPDX-License-Identifier: GPL-3.0-or-later
# turzx-control — turing-smart-screen-python(GPL-3.0-or-later)을 레퍼런스로 사용한다.
import sys, time, usb.util; sys.path.insert(0, 'tss')
from PIL import Image, ImageDraw
import library.lcd.lcd_comm_turing_usb as T

dev, pid = T.find_usb_device()
T.send_sync_command(dev)
cfg = dev.get_active_configuration()
intf = usb.util.find_descriptor(cfg, bInterfaceNumber=0)
ep_out = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress)==usb.util.ENDPOINT_OUT)
ep_in  = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress)==usb.util.ENDPOINT_IN)
W, H = 1920, 462

def frame(i):
    img = Image.new("RGB", (W, H))
    d = ImageDraw.Draw(img)
    for x in range(0, W, 8):
        v = (x + i*24) % 512; v = v if v < 256 else 511-v
        d.rectangle([x,0,x+8,H], fill=(v//3, v, 255-v))
    return img.transpose(Image.Transpose.ROTATE_270)

blobs = [T._encode_png(frame(i)) for i in range(40)]

def packet(png):
    p = T.build_command_packet_header(T.CMD_UPLOAD_PNG)
    n = len(png); p[8],p[9],p[10],p[11] = (n>>24)&255,(n>>16)&255,(n>>8)&255,n&255
    return T.encrypt_command_packet(p) + png

def run(label, read_resp, flush):
    pkts = [packet(b) for b in blobs]
    t0 = time.perf_counter()
    for pk in pkts:
        ep_out.write(pk, 2000)
        if read_resp:
            try: ep_in.read(512, 2000)
            except Exception as e: print("  read err:", e); break
        if flush: T.read_flush(ep_in)
    t1 = time.perf_counter()
    print(f"{label:34s} {len(pkts)/(t1-t0):6.1f} fps")

run("응답 read + read_flush (현재)", True, True)
run("응답 read만 (flush 제거)",      True, False)
run("write만 (응답 무시)",           False, False)
T.read_flush(ep_in)
