# TURZX Control

TURZX Control은 Turing USB 스마트 스크린에 AI 사용량과 하드웨어 상태를 표시하는
Windows 중심 크로스플랫폼 데몬이다. Windows 데몬이 USB와 센서를 소유하고, 로컬
컨트롤 UI가 전용 Codex·Claude 프로필의 사용량을 수집한다.

현재 표시 지표는 Codex·Claude의 5시간/주간 잔여량과 각 리셋까지의 시간, CPU·GPU·RAM
사용률과 온도다. RAM 온도가 지원되지 않으면 식별된 메인보드 온도를 사용하고 출처를
표시한다. 하드웨어 수집과 화면 합성은 1초 간격, 사용량 조회는 30초 간격이다.

## 현재 범위

- `cmd/turzx-control`: 로컬 설정 UI, Windows 트레이, USB 화면 출력, 공급자 연결 관리
- `cmd/turzx-claude-status`: Claude statusline 허용 필드를 전용 inbox로 전달
- `cmd/turzx-probe`: USB·PNG·영상 경로를 확인하는 독립 진단 도구
- `cmd/turzx-metrics`, `cmd/turzx-codex`: USB 없이 센서와 Codex 수집기를 진단하는 CLI
- `tools/turzx-sensors`: Windows 권한 상승 센서 보조 프로그램
- 기본 테마는 `smon-halloween`이며 `azure-ribbon`도 지원한다.

Windows 컨트롤은 `설정 열기`와 `종료` 트레이 메뉴를 제공한다. 자동 시작은 현재
사용자의 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`에 실행 파일 경로를
등록하는 방식이며, 설치 위치를 바꿀 때는 기존 등록을 먼저 해제한다.

standalone ZIP, 설치 프로그램, 코드 서명과 최종 배포용 고지는 아직 완료되지 않았다.
공식 `claude`·`codex` CLI와 사용자 자격 증명은 배포물에 포함하지 않는다.
남은 구현·검증 항목은 [`PROGRESS.md`](PROGRESS.md)에서 관리한다.

## Windows 개발 환경

필요한 도구는 Go, MSYS2 UCRT64의 GCC·`pkg-config`·libusb, .NET 8 SDK, FFmpeg다.
.NET SDK·NuGet·PawnIO 설치본은 저장소의 `.tools` 아래에 둔다. MSYS2와 FFmpeg처럼
저장소 밖에 있는 도구의 실제 경로는 환경마다 다르므로 `AGENTS.md`의 로컬 개발 환경
절에서 관리한다. USB 빌드에는 `CGO_ENABLED=1`과 UCRT64 컴파일러가 필요하다.

```powershell
$repo = (Resolve-Path .).Path
$msys = '<MSYS2 설치 경로>'   # AGENTS.md 로컬 개발 환경 절 참조
$env:Path = "$msys\ucrt64\bin;$env:Path"
$env:CGO_ENABLED = '1'
$env:CC = "$msys\ucrt64\bin\gcc.exe"
$env:PKG_CONFIG = "$msys\ucrt64\bin\pkg-config.exe"
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { $go = 'C:\Program Files\Go\bin\go.exe' }
& $go build -ldflags '-H=windowsgui' -o bin\turzx-control.exe .\cmd\turzx-control
& $go build -o bin\turzx-claude-status.exe .\cmd\turzx-claude-status
```

Windows 실행 파일 옆에 MSYS2 UCRT64의 `bin\libusb-1.0.dll`을 둔다. 영상 재생에는
FFmpeg `bin\ffmpeg.exe`를 지정한다. FFmpeg 실행 파일은 별도 라이선스 고지와 함께
동봉해야 한다.

## 실행 설정

배경은 존재하는 절대 경로의 `.mp4` 파일이어야 한다. 저장할 비자격 증명 설정은
`-save-config`로 사용자 설정 디렉터리의 `settings.json`에 기록한다.

```powershell
$background = (Resolve-Path '.\assets\backgrounds\smon-halloween.mp4').Path
$ffmpeg = 'C:\tools\ffmpeg\bin\ffmpeg.exe'
& $go run .\cmd\turzx-control `
  -background $background -ffmpeg $ffmpeg -theme smon-halloween `
  -save-config
```

주요 표시 옵션은 `-brightness`, `-chunk-wait`, `-sensor-helper`,
`-sensor-snapshot`과 센서 선택 ID들이다. `-sensor-helper`와 `-sensor-snapshot`은
동시에 지정하지 않는다. 기본 chunk 대기 상한은 3초다.

## 전용 공급자 프로필

컨트롤은 기존 CLI·앱 프로필이나 WSL 프로필을 탐색하거나 병합하지 않는다. 기본값은
사용자 설정 루트 아래의 `turzx-control\codex`, `turzx-control\claude`,
`turzx-control\inbox\claude\<GOOS>\default`이며, 필요하면 다음 옵션으로 경로를
명시한다.

```text
-codex-bin <codex 실행 파일>
-codex-home <전용 CODEX_HOME>
-claude-bin <claude 실행 파일>
-claude-config-dir <전용 CLAUDE_CONFIG_DIR>
-claude-status-bin <turzx-claude-status 실행 파일>
-claude-inbox-dir <전용 statusline inbox>
```

로그인은 설정 UI에서 공식 Codex App Server와 Claude Code 흐름으로 시작한다. 제품 코드는
인증 파일을 직접 읽지 않는다. Codex는 전용 `CODEX_HOME`의 App Server API를 사용하고,
Claude는 전용 `CLAUDE_CONFIG_DIR`에서 statusline을 받아 inbox에 기록한다.

Claude statusline sidecar의 확인 정보는 로컬 binding 세대와 명령·inbox 경로의 일치만
기록한다. 공식 인증 성공 뒤에만 확인하며, 로그아웃 시작 시 확인을 먼저 무효화한다.
재시작 때 command와 inbox가 바뀌면 이전 계정으로 자동 복원하지 않는다.

## Windows 센서와 최초 UAC

센서 helper는 self-contained .NET publish 디렉터리로 준비한다.

```powershell
powershell.exe -NoProfile -File .\scripts\publish-sensors.ps1 `
  -DotnetPath .\.tools\dotnet\dotnet.exe `
  -OutputPath .\artifacts\sensors-task-<date>
powershell.exe -NoProfile -File .\scripts\test-sensor-task.ps1 `
  -HelperDirectory .\artifacts\sensors-task-<date>
```

`test-sensor-task.ps1`는 설치하지 않는 읽기 전용 검사다. 실제 설치는 같은 Windows
사용자로 다음을 실행한다.

```powershell
powershell.exe -NoProfile -File .\scripts\setup-sensor-task.ps1 `
  -Action Install -HelperDirectory .\artifacts\sensors-task-<date>
```

현재 설치 스크립트는 검토된 개발 helper의 manifest 해시를 고정하므로 임의의 새
publish는 거부한다. 새 빌드는 검토·self-test 후 설치 스크립트의 고정 해시를 갱신해야
하며, 실행 시 계산한 해시를 그대로 신뢰하도록 바꾸지 않는다.

`-InstallRoot`를 주면 helper와 snapshot을 관리자가 고른 한 디렉터리 아래로 모은다.
`-AppDirectory`를 함께 주면 컨트롤 앱 payload도 같은 루트에 설치한다. 압축을 푼
자리에서 그대로 실행하는 포터블 사용도 그대로 가능하며, 이 옵션은 설치 위치를
한 곳으로 모으고 싶을 때만 쓴다.

```powershell
powershell.exe -NoProfile -File .\scripts\setup-sensor-task.ps1 `
  -Action Install -HelperDirectory .\artifacts\sensors-task-<date> `
  -AppDirectory .\bin -InstallRoot D:\TURZX
```

이때 helper는 `<루트>\Sensors\<버전>`, snapshot은 `<루트>\Data\Sensors\<SID>`,
앱은 `<루트>\App`에 놓인다. 앱 디렉터리도 관리자 소유 보호 디렉터리이므로
표준 사용자는 실행만 할 수 있고, 앱을 갱신하려면 설치를 다시 실행해 UAC를 승인해야
한다. 앱 payload는 사용자 권한으로 실행하므로 helper와 달리 해시를 고정하지 않는다.
설치 후 출력의 `app` 경로가 실제 실행 파일이며, 로그인 자동 시작은 그 실행 파일로
등록한다. 루트와 그 상위 디렉터리는 관리자 소유여야 하며, 비관리자가 교체할 수 있는
상위가 있으면 설치를 거부한다. 데몬도 같은 규칙으로 snapshot을 검증하므로 경로만
바뀌고 신뢰 조건은 같다. 없는 상위 디렉터리는 관리자가 먼저 만들어야 한다.

설치 시 UAC를 한 번 승인하면 helper가 보호된 디렉터리(기본값 `Program Files` 하위)에
복사되고, 기본값 기준 `ProgramData\TURZXControl\Sensors\<현재 사용자 SID>\snapshot.json`을
갱신하는 로그온 예약 작업이 등록된다. 작업은 최고 권한·대화형 사용자 토큰으로 실행되며 이후
로그온마다 자동 시작한다. 컨트롤은 관리자 권한을 요구하지 않고 보호된 snapshot만
읽는다. snapshot은 소유자·DACL·재분석 지점·신선도를 검증한다.

`-IncludePawnIO`를 사용한 publish는 고정 SHA256과 Authenticode 서명을 확인하고
설치 파일을 동봉할 뿐, 드라이버를 자동 설치하지 않는다. 제거는 예약 작업을 해제하고
이 스크립트가 설치한 helper·앱·snapshot 디렉터리를 지우며 비게 된 상위 디렉터리까지
정리한다. 다른 앱과 공유하는 PawnIO 드라이버는 건드리지 않는다. 사용자 지정 루트에
설치했다면 제거할 때도 같은 `-InstallRoot`를 준다.

컨트롤 자체의 사용자 로그인 자동 시작은 다음과 같이 관리한다. `settings.json`이
있으면 등록 전에 그 내용을 검증하므로, 실행 옵션을 바꿨다면 먼저 `-save-config`로
저장한다. 파일이 없으면 컴파일된 기본값으로 등록한다.

```powershell
.\turzx-control.exe -autostart enable
.\turzx-control.exe -autostart status
.\turzx-control.exe -autostart disable
```

## 검증

Go 검증은 USB 장치 없이 수행할 수 있다.

```powershell
& $go fmt .\...
& $go vet .\...
& $go test .\...
& $go build .\...
```

USB 대상 검사는 `cmd/turzx-probe`를 사용하며, 시작 시 장치 응답을 읽고 sync·PNG·영상
경로를 순서대로 확인한다. 실제 장치 검사는 한 번에 하나의 프로세스로 실행한다.
`scripts\check-windows.ps1`는 OS·도구·USB 드라이버 상태를 읽기 전용으로 진단한다.
센서 publish 결과에는 `turzx-sensors.exe --self-test`가 실행된다.

테스트가 실패하면 먼저 Go·CGO·UCRT64 경로와 FFmpeg/libusb 파일을 확인하고, 해당 OS에서
실행하지 못한 검사는 결과에 명시한다.

## 라이선스와 배포 고지

프로젝트는 **GPL-3.0-or-later**다. USB 장치 프로토콜 구현은
[`turing-smart-screen-python`](https://github.com/mathoudebine/turing-smart-screen-python)
의 GPL-3.0-or-later 구현을 바탕으로 한다. 배포 시 루트 [`LICENSE`](LICENSE)를 포함한다.

센서 helper는 LibreHardwareMonitor 계열 및 기타 NuGet 구성 요소를 사용하고, FFmpeg와
libusb도 별도 고지가 필요하다. 최종 standalone ZIP을 만들기 전에 각 동봉 파일의
라이선스·저작권·소스 제공 의무를 `THIRD-PARTY-NOTICES.txt`로 정리해야 한다.
테마 배경과 정의는 `assets/backgrounds/<테마 ID>.mp4`와 `<테마 ID>.theme.json`으로
함께 추적한다. `theme.json`은 디자인 참조 문서이며 데몬은 레이아웃을
`internal/render`의 Go 코드에서 그린다. 설정 UI는 127.0.0.1에만 열리지만 같은 PC의
다른 프로세스와 사용자 계정은 접근할 수 있으므로 공유 PC에서는 이 점을 고려한다. 사용자 제공 배경의 외부 재배포 권리는 공개 배포 전에 별도로 확인한다.
빌드 실행 파일과 설치 패키지 ZIP은 루트 `bin/`에 두며 Git에서 제외한다.

standalone ZIP은 다음으로 만든다. 앱 payload, 센서 helper publish, 설치 스크립트,
동봉 구성 요소의 라이선스 고지를 한데 모은다.

```powershell
powershell.exe -NoProfile -File .\scripts\package-zip.ps1 `
  -HelperDirectory .\artifacts\sensors-task-<date> `
  -FFmpegLicense '<FFmpeg 빌드>\LICENSE' `
  -LibusbLicense '<MSYS2>\ucrt64\share\licenses\libusb\COPYING' `
  -SourceOffer '<corresponding source 제공 경로>' `
  -OutputPath .\bin\turzx-control-dev-<date>.zip
```

`-SourceOffer`는 필수다. GPL·LGPL 바이너리를 동봉하므로 corresponding source를
어떻게 제공하는지 적지 않은 배포물은 조건을 만족하지 못한다. Go 모듈과 NuGet
패키지의 고지는 모듈 캐시와 `.nuspec`에서 모으며, 라이선스를 찾지 못하면 패키징이
실패한다. 스크립트는 ZIP에 넣은 설치 스크립트의 고정 해시가 같이 넣은 helper와
일치하는 상태를 그대로 담으므로, 받은 쪽에서 설치할 때 payload가 검증된다.
출력물은 서명되지 않은 개발 빌드이며 정식 배포본이 아니다.
테스트와 코드만으로 기능 검증을 재현할 수 있어야 한다.
