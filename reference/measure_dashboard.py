import sys, time, math, statistics, usb.core, usb.util; sys.path.insert(0,'tss')
from PIL import Image, ImageDraw, ImageFont
import library.lcd.lcd_comm_turing_usb as T
dev,_ = T.find_usb_device()
cfg = dev.get_active_configuration(); intf = usb.util.find_descriptor(cfg, bInterfaceNumber=0)
ep_out = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress)==usb.util.ENDPOINT_OUT)
ep_in  = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress)==usb.util.ENDPOINT_IN)
def drain(ms=40,cap=600):
    n=0
    for _ in range(cap):
        try: ep_in.read(512,ms); n+=1
        except usb.core.USBError: break
    return n
def resync(): drain(); T.send_sync_command(dev); time.sleep(0.25); drain()

W,H=1920,462
big=ImageFont.truetype("/System/Library/Fonts/Supplemental/Arial Bold.ttf",96)
sml=ImageFont.truetype("/System/Library/Fonts/Supplemental/Arial.ttf",34)
def dash(i, wave=True):
    t=i/12.0
    img=Image.new("RGBA",(W,H),(10,12,20,255)); d=ImageDraw.Draw(img)
    if wave:
        for x in range(0,W,6):
            y=H/2+math.sin(x/180+t*2.2)*130+math.sin(x/70-t*1.4)*45
            c=int(90+90*math.sin(x/300+t)); d.line([(x,y),(x,H)],fill=(c//4,c,150,255),width=6)
    for k,(lab,val) in enumerate([("CLAUDE 5h","73%"),("CODEX 5h","91%"),("CPU","47C"),("GPU","61C")]):
        x=70+k*470
        d.text((x,60),lab,fill=(150,160,180,255),font=sml)
        d.text((x,100),val,fill=(255,255,255,255),font=big)
    return img.transpose(Image.Transpose.ROTATE_270)
def pkt(png):
    p=T.build_command_packet_header(T.CMD_UPLOAD_PNG); n=len(png)
    p[8],p[9],p[10],p[11]=(n>>24)&255,(n>>16)&255,(n>>8)&255,n&255
    return T.encrypt_command_packet(p)+png

print("실제 대시보드 화면(RGBA) 지속 fps  N=40")
print(f"{'화면':<16}{'프레임':>9} {'flush':>7} {'fps':>7} {'p95':>7}  결과")
for wave,label in ((False,"단색 배경"),(True,"움직이는 물결")):
    pngs=[T._encode_png(dash(i,wave)) for i in range(40)]
    avg=sum(len(p) for p in pngs)/len(pngs); pkts=[pkt(p) for p in pngs]
    for fms in (3, 10):
        resync(); lat=[]; fail=None; t0=time.perf_counter()
        for i,pk in enumerate(pkts):
            a=time.perf_counter()
            try:
                ep_out.write(pk,4000); ep_in.read(512,3000)
                for _ in range(5):
                    try: ep_in.read(512,fms)
                    except usb.core.USBError: break
            except usb.core.USBError:
                fail=i; break
            lat.append((time.perf_counter()-a)*1000)
        el=time.perf_counter()-t0
        if fail is None:
            print(f"{label:<16}{avg/1024:8.1f}KB {fms:5d}ms {len(pkts)/el:7.1f} {sorted(lat)[int(len(lat)*0.95)]:6.0f}ms  완주")
        else:
            print(f"{label:<16}{avg/1024:8.1f}KB {fms:5d}ms {'-':>7} {'-':>7}  frame {fail} 실패")
        drain()
resync()
