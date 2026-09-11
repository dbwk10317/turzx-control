# turzx-control

TURZX / Turing USB 스마트 스크린 패널에 Claude·Codex 사용량과 시스템 센서를 띄우는
크로스플랫폼 데몬. Windows·macOS·Linux에서 동작하도록 만든다.

**현재 상태: G0/G1 검증 도구 구현 착수.** 패킷 생성, 장치용 RGBA PNG 인코딩·검사와
Windows 환경 진단, USB sync/PNG/유한 H264 전송과 실시간 진단 오버레이 도구가 있다. 제품 데몬은 아직 없다.
평가·라이브러리 선택·Go 구현 원칙·빌드/서명/배포 계획은 [`DESIGN.md`](DESIGN.md)에 있다.

## 무엇이 확인되었나

- TURZX 9.2"(VID `0x1cbe` / PID `0x0092`, 462×1920)에서 화면 출력과 H264 비디오 재생 확인
- sync·밝기·PNG/JPEG 헤더를 Go로 포팅하고 업스트림 기준 패킷과 바이트 단위 일치를 검증
- macOS에서 낱장 PNG 전송과 파일 H264 재생·인코딩의 짧은 실측 기록 확보 (`DESIGN.md` 2~3절)

사무실에서는 Windows 데몬이 USB와 시스템 센서를 담당하고, Windows/WSL에서 실행한
Claude·Codex의 사용량을 공급자별로 수집한다. 기본 지표는 gopsutil, Windows 온도·GPU는
LibreHardwareMonitor, 영상은 FFmpeg를 활용하는 방향이다. 다음 단계와 통과 기준은
`DESIGN.md` 8절에 있다.

## 개발·검증

USB 코드는 `gousb`/cgo와 libusb 1.0을 사용한다. Windows에서는 MSYS2 UCRT64의
`mingw-w64-ucrt-x86_64-gcc`, `mingw-w64-ucrt-x86_64-libusb`,
`mingw-w64-ucrt-x86_64-pkgconf`가 필요하다. MSYS2 업데이트 후 이 패키지를 설치한다.
다음은 이번 개발 PC 경로의 예시이며 환경 변수는 현재 PowerShell에만 적용된다.

```powershell
$env:Path = "C:\Workspace\tools\msys64\ucrt64\bin;" + $env:Path
$env:CGO_ENABLED = '1'
$env:CC = 'C:\Workspace\tools\msys64\ucrt64\bin\gcc.exe'
$env:PKG_CONFIG = 'C:\Workspace\tools\msys64\ucrt64\bin\pkg-config.exe'
go test ./...
go vet ./...
go build ./...
powershell.exe -NoProfile -File scripts/check-windows.ps1 -MsysRoot C:\Workspace\tools\msys64 -FFmpegPath C:\Workspace\tools\ffmpeg-9.0.1-essentials_build\bin\ffmpeg.exe
```

Windows 진단은 OS·CPU·GPU·도구 경로와 대상 USB의 드라이버 바인딩을 JSON으로 출력한다.
드라이버·계정·설정을 변경하지 않는다. 조회 실패는 `errors`와 종료 코드 1로 구별한다.
도구 경로가 null이면 PATH와 명시된 기본 경로에서 찾지 못한 것이며 미설치를 단정하지 않는다.
장치가 열거되고 WINUSB가 표시돼도 일반 사용자 USB 송수신 검증이 끝난 것은 아니다.

`internal/turzx.EncodePNG`는 회전이 끝난 462×1920 이미지를 8bit RGBA로 인코딩한다.
`ValidatePNG`는 형식·크기·페이로드 상한과 PNG 손상을 검사한다. 기존 `ImageCommand`는
저수준 패킷 생성 함수이므로 외부 PNG는 별도로 검증한 뒤 넘겨야 한다.
G1에는 로컬 기성 모션 배경, ffmpeg, libusb/C 툴체인과 실제 패널 표시 지연 측정이 필요하다.

### 최소 USB 검증

벤더 앱을 종료하고 위 PowerShell 환경에서 실행한다. 기본 명령은 IN drain과 sync만 수행한다.
명시적인 `-test-pattern`은 화면을 네 색상 띠로 바꾸며, `-png`는 지정한 네이티브 RGBA PNG를 표시한다.
`-h264`는 유한한 Annex B H264 파일로 업스트림의 초기화·청크 협상·큐 조회·정지 순서를 검증한다.

```powershell
go run ./cmd/turzx-probe
go run ./cmd/turzx-probe -test-pattern
go run ./cmd/turzx-probe -png C:\path\native-rgba.png
go run ./cmd/turzx-probe -h264 C:\path\finite-annex-b.h264
```

기성 배경을 준비하기 전에는 아래 8초 진단 영상으로 유한 전송만 확인할 수 있다.
이 패턴은 첫 테마나 G1의 실제 모션 배경을 대신하지 않는다. `-n`은 기존 파일 덮어쓰기를 막는다.

```powershell
& 'C:\Workspace\tools\ffmpeg-9.0.1-essentials_build\bin\ffmpeg.exe' -hide_banner -loglevel error -n -f lavfi -i 'testsrc2=size=1920x462:rate=25' -t 8 -vf transpose=1 -an -c:v libx264 -preset veryfast -tune zerolatency -pix_fmt yuv420p -bf 0 -g 25 -b:v 1500k -f h264 g1-finite-test.h264
Get-FileHash g1-finite-test.h264 -Algorithm SHA256
go run ./cmd/turzx-probe -h264 g1-finite-test.h264
```

응답과 경과 시간은 JSON으로 출력된다. 성공 응답은 실제 화면 표시 확인을 대신하지 않는다.
`-timeout` 기본값은 I/O별 2초, `-flush-timeout`은 읽기별 20ms이며 검증용 초기값이다.
drain은 횟수·총시간 상한에 도달하면 실패한다. 실패한 패킷은 자동 재전송하지 않는다.
H264 모드는 기본 25fps, 밝기 32, 큐 대기 상한 1500ms이며 각각 `-frame-rate`, `-brightness`,
`-queue-timeout`으로 시험값을 바꿀 수 있다. H264 모드는 밝기를 바꾸고 종료 시 스트림 정지를 시도한다.
그 밖의 모드는 밝기를 바꾸지 않는다. 이 도구는 드라이버 교체·영구 설정 저장을 하지 않으며 종료 후
시험 화면은 남을 수 있다. H264 경로는 단위 테스트와 Windows 장치 응답을 확인했고 사용자가 진단 영상의 전체 화면 정상 재생을 확인했다.
현재 마지막 청크 뒤에도 큐 값이 3 이하이면 정지하므로 영상 끝까지 재생됐음을 보장하지 않는다.
현재는 개발 환경에서 실행하는 검증 도구이며 서명·DLL 동봉을 완료한 배포물이 아니다.

### 실시간 경로 검증

`-background`는 로컬 MP4를 반복하고 2초마다 바뀌는 진단 카운터를 PNG로 그려
25fps로 FFmpeg에 공급한다. 합성 영상을 회전해 Annex B H264로 출력한다.
PNG 디코더는 1스레드, 인코더는 libx264 1.5Mbit/s CBR을 사용한다. 프레임 공급은
호스트의 PNG 입력으로 제한하며 배경 입력에 별도의 `-readrate` 제한을 겹치지 않는다.
진단 화면은 첫 테마 시안이 아니다. 실제 기성 배경을 아직 준비하지 못했다면 아래처럼
짧은 시험 소재를 만들 수 있지만, 실제 배경 검증을 통과한 것으로 보지 않는다.

```powershell
& 'C:\Workspace\tools\ffmpeg-9.0.1-essentials_build\bin\ffmpeg.exe' -hide_banner -loglevel error -n -f lavfi -i 'testsrc2=size=1920x462:rate=25' -t 8 -an -c:v libx264 -preset veryfast -pix_fmt yuv420p -g 25 -b:v 1500k g1-diagnostic-background.mp4

# USB 없이 인코더 출력 확인. 출력 파일은 새 파일이어야 한다.
go run ./cmd/turzx-probe -background g1-diagnostic-background.mp4 -ffmpeg C:\Workspace\tools\ffmpeg-9.0.1-essentials_build\bin\ffmpeg.exe -duration 12s -render-only g1-render-output.h264

# 패널 실시간 전송. 기본 밝기는 32이며 30초 뒤 취소·정지를 수행한다.
go run ./cmd/turzx-probe -background g1-diagnostic-background.mp4 -ffmpeg C:\Workspace\tools\ffmpeg-9.0.1-essentials_build\bin\ffmpeg.exe -duration 30s
```

`-chunk-wait` 기본값 1500ms는 첫 바이트 대기부터 청크 조립·USB 송신 시작 전까지의 상한이다.
협상된 크기를 채운 청크만 전송하고, 부족한 바이트를 패딩하거나 압축 스트림 일부를 버리지 않는다.
EOF·정체·취소 시 인코더와 파이프를 정리하고 장치 정지를 시도한다. 자동 재시도·PNG 폴백은 아직 없다.
기간 종료도 취소로 처리하므로 `duration_reached`와 오류 내용을 함께 확인한다. 종료 코드만으로
지연 검증 통과를 판정하지 않는다. 강제 종료 시 저장한 H264의 끝부분은 불완전할 수 있다.

현재 Windows 진단 시험에서는 움직이는 배경 30초·저변화 배경 15초 전송이 각각
최대 청크 대기 1121ms·1081ms로 기간 종료까지 진행됐고 정지 응답을 확인했다.
이후 실제 `assets/backgrounds/azure-ribbon.mp4`로 1분 전송과 사용자의 영상·카운터 정상 표시를 확인했다.

stdout에는 소재 SHA-256·인코더 인자·청크/큐 값·경과 시간·정지 응답을 JSON으로 남긴다.
stderr의 `overlay`는 진단 샘플 생성 시각이다. 청크 대기시간이나 전송 응답은 패널 표시 지연을
대신하지 않는다. 사용자 결정에 따라 30분 연속 검증은 구현의 선행 조건에서 제외했다.
장시간 안정성과 표시 지연의 수치 검증은 후속 실제 사용에서 확인한다.

### 하드웨어 지표 수집

```powershell
go run ./cmd/turzx-metrics -samples 5
# 종료할 때까지 1초 간격으로 수집
go run ./cmd/turzx-metrics -samples 0
```

USB를 열지 않고 CPU·RAM 사용률을 실제 호스트에서 수집해 JSON으로 출력한다.
CPU 첫 표본은 비교할 이전 표본이 없어 `collecting`이며 값은 null이다.
GPU 사용률과 CPU·GPU·RAM 온도는 별도 센서 소스를 연결하기 전까지 `unconnected`다.
이는 센서 미지원 판정이 아니다. RAM 온도 미지원이 확인되면 식별된 메인보드 온도로 대체하고
라벨에도 실제 출처를 표시하는 규칙은 설계에 반영했으며, 센서 연결·대체 구현은 후속 단계다.

제품의 갱신 주기는 하드웨어 수집·표시 1초, Claude·Codex 사용량 표시 5초다.
사용량 원본 조회/훅 수신은 별도이며 현재 진단 LIVE 카운터는 기존 2초 시험 설정을 유지한다.

## 라이선스

GPL-3.0-or-later.

이 프로젝트의 장치 프로토콜 구현은
[mathoudebine/turing-smart-screen-python](https://github.com/mathoudebine/turing-smart-screen-python)
(Copyright © 2021 Matthieu Houdebine, GPL-3.0-or-later)의 구현을 참조해 Go로 옮긴 것이다.
`internal/turzx/testdata/golden.json`의 기준 패킷도 해당 구현으로 생성했다. 원 저작자와 기여자들에게 감사한다.
