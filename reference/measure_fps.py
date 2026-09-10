import sys, time, random, statistics, usb.core, usb.util; sys.path.insert(0,'tss')
from PIL import Image, ImageDraw
import library.lcd.lcd_comm_turing_usb as T

dev,_ = T.find_usb_device()
cfg = dev.get_active_configuration()
intf = usb.util.find_descriptor(cfg, bInterfaceNumber=0)
ep_out = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress)==usb.util.ENDPOINT_OUT)
ep_in  = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress)==usb.util.ENDPOINT_IN)
def drain(ms=40, cap=600):
    n=0
    for _ in range(cap):
        try: ep_in.read(512, ms); n+=1
        except usb.core.USBError: break
    return n
def resync(): drain(); T.send_sync_command(dev); time.sleep(0.25); drain()

W,H = 1920,462
def frame(detail, i):
    img = Image.new("RGBA",(W,H),(8,10,16,255)); d=ImageDraw.Draw(img)
    r = random.Random(i*7+detail)
    for k in range(detail):
        x=(k*97+i*13)%W
        d.rectangle([x, r.randrange(H), x+r.randrange(4,40), H],
                    fill=(r.randrange(256), r.randrange(256), 200, 255))
    return img.transpose(Image.Transpose.ROTATE_270)
def pkt(png):
    p=T.build_command_packet_header(T.CMD_UPLOAD_PNG); n=len(png)
    p[8],p[9],p[10],p[11]=(n>>24)&255,(n>>16)&255,(n>>8)&255,n&255
    return T.encrypt_command_packet(p)+png

print("RGBA 재측정 (N=100, 응답 1:1 대기, flush 없음)")
print(f"{'프레임':>10} {'지속fps':>8} {'p50':>7} {'p95':>7}  결과")
for detail in (30, 200, 800, 2500, 6000, 15000, 40000):
    resync()
    pngs=[T._encode_png(frame(detail,i)) for i in range(100)]
    avg=sum(len(p) for p in pngs)/len(pngs); pkts=[pkt(p) for p in pngs]
    lat=[]; fail=None; t0=time.perf_counter()
    for i,pk in enumerate(pkts):
        a=time.perf_counter()
        try:
            ep_out.write(pk, 3000); ep_in.read(512, 2000)
        except usb.core.USBError:
            fail=i; break
        lat.append((time.perf_counter()-a)*1000)
    el=time.perf_counter()-t0
    if fail is None:
        print(f"{avg/1024:9.1f}KB {len(pkts)/el:8.1f} {statistics.median(lat):6.0f}ms {sorted(lat)[int(len(lat)*0.95)]:6.0f}ms  완주")
    else:
        print(f"{avg/1024:9.1f}KB {'-':>8} {'-':>7} {'-':>7}  frame {fail} 실패")
    drain()
resync()
