# reference — 실측 도구

`DESIGN.md` 1절의 모든 수치가 여기서 나온다. 값을 의심하거나 다른 환경(특히 사무실 Windows PC)에서
다시 재야 할 때 이 스크립트들을 쓴다.

## 준비

업스트림 구현을 레퍼런스로 쓴다. 이 저장소에 넣지 않고 필요할 때 받는다.

```bash
git clone --depth 1 --branch 3.10.0 https://github.com/mathoudebine/turing-smart-screen-python.git tss
python3 -m venv venv && ./venv/bin/pip install pyusb pillow pycryptodome pyserial
brew install libusb          # Linux는 배포판 패키지, Windows는 external/libusb-1.0/libusb-1.0.dll
```

## 스크립트

| 파일 | 장치 필요 | 무엇을 재나 |
|---|---|---|
| `measure_encoding.py` | 아니오 | 실제 대시보드 화면의 프레임 크기 |
| `measure_bottleneck.py` | 예 | 응답 처리 방식별 처리량 (병목이 flush임을 보인다) |
| `measure_flush.py` | 예 | flush 타임아웃 × 프레임 크기별 지속 fps |
| `measure_fps.py` | 예 | 프레임 크기별 지속 fps와 지연 분포 |
| `measure_dashboard.py` | 예 | **실제 대시보드 화면의 지속 fps** — 설계 결론의 근거 |
| `gen_golden.py` | 아니오 | 업스트림 패킷을 `testdata/golden.json`으로 덤프 (Go 포팅 검증 기준) |

## 주의

**PNG는 반드시 RGBA로 인코딩한다.** 모든 스크립트가 `Image.new("RGBA", ...)`를 쓰는 이유다.
장치는 RGB와 팔레트(P) PNG를 조용히 거부한다 — 전송은 `OK`를 응답하지만 RGB는 화면이 세로
1/4로 축소되어 4번 반복되고 배경이 갱신되지 않으며, 팔레트는 아예 무시된다. 오류로 드러나지
않으므로 측정값이 통째로 무의미해질 수 있다. 실제로 초기 측정 전체를 RGB로 수행해 폐기했다.

장치를 이상 상태로 만들 수 있다. **응답을 읽지 않고 write만 하는 코드를 돌리지 말 것** —
stale 응답이 쌓여 이후 모든 전송이 타임아웃난다. 그렇게 되면 IN 큐를 전부 비우고 sync를
다시 보내야 하고, 그래도 안 되면 USB를 뽑았다 꽂는다. 각 측정 앞뒤로 큐를 비우는 이유가 이것이다.
