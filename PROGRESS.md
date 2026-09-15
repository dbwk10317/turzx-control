**이 작업이 끝나면 이 문서는 삭제한다.**

남은 검증과 배포 작업:

- 재개 시 `README.md`의 환경으로 빌드한다. 테마 원본은 `assets/backgrounds/`, 빌드·ZIP 출력은 Git에서 제외한 `bin/`을 사용한다. 앱은 정리 작업을 위해 종료했으며 자동 재실행하지 않는다.
- 로컬 정리 시 `.tools/`(개발 도구), `artifacts/sensors-task-20260914/`(고정 해시의 센서 설치 입력), `artifacts/dev-claude-connect-20260911/`(기존 Claude 훅 어댑터)는 재구성·재연결 전까지 보존한다. 이 의존성을 새 빌드·설치 경로로 옮긴 뒤 제거한다.

- Claude 재연결과 실제 재로그인 후 전용 프로필 사용량 수신 복구를 다시 확인한다.
- USB 분리·재연결, 응답 복구, PNG fallback, 지연 및 중단 경로를 측정한다.
- 마지막 프레임까지의 유한 스트림 재생과 안정성·장기 실행 검증을 수행한다. 승인되지 않은 무인 장기 실행은 자동 재시작하지 않는다.
- Windows 실제 설치에서 로그인 자동 시작 enable·disable·status와 재부팅 후 복구를 검증한다.
- standalone ZIP은 `scripts/package-zip.ps1`로 만든다. 앱·helper·설치 스크립트·라이선스 고지를 담고, 받은 쪽에서 푼 ZIP의 설치 스크립트가 같이 담긴 helper를 고정 해시로 검증하는 것까지 확인했다(90.8MB, 고지 27건). 남은 것: Authenticode 서명과 SmartScreen 평판, 깨끗한 Windows PC 설치 확인, 그리고 아래 두 가지 배포 결정.
- 첫 설정 UI에서 센서 선택, snapshot 경로 계약, 표준 사용자 동작과 최초 관리자 승인 흐름을 확인한다.
- Windows/macOS/Linux별 빌드·실행·권한·서명 검증을 수행한다.
- 라이선스 고지는 ZIP의 `NOTICES/`에 들어가고 `-SourceOffer` 없이는 패키징이 안 된다. FFmpeg는 `scripts/build-ffmpeg.sh`로 직접 빌드해 동봉한다. WSL(Rocky 9)에서 mingw-w64로 크로스 빌드하고 Windows에서 실제 파이프라인으로 검증했다 — 7.5MB, ffmpeg `bf1b838f2a`(n9.0.1), x264 `b35605ac`. 고정 커밋과 이 스크립트가 corresponding source의 완전한 빌드 방법이므로 제공 조건이 우리 손 안에서 해결된다. 산출물은 `artifacts/ffmpeg-win-x64-20260914/`에 라이선스 원문과 함께 보관한다. 남은 것: `-SourceOffer`에 실제 소스 제공 경로 넣기, Authenticode 서명. MSYS2 네이티브 빌드는 configure의 임시 탐지 실행 파일을 시만텍이 격리해 쓸 수 없었다. WSL 네이티브 경로에서 빌드하면 Windows 파일시스템을 거치지 않아 탐지가 전혀 없다. 다른 PC에서 재현할 때도 같은 방법을 쓴다.
- 2026-09-14 리뷰 반영 helper를 `artifacts/sensors-task-20260914-owner`로 publish하고 `setup-sensor-task.ps1`의 manifest 해시를 갱신했다. 리뷰에서 들어간 `WriteAtomic`의 owner 설정이 쓰기 핸들에 `WRITE_OWNER`가 없어 모든 snapshot 쓰기를 실패시켰다(태스크 종료 코드 1). 닫힌 파일에서, 그것도 기본 정책으로 이미 관리자 소유가 아닐 때만 설정하도록 고쳤다. 남은 확인: 두 번째 Windows 사용자 계정의 snapshot 읽기(`BUILTIN\Users` 읽기 ACE)와 예약 작업 재시작 설정.
- Claude CLI 실제 동작 확인이 필요한 항목: `claude auth login --claudeai`가 headless로 URL을 출력하는지(출력하면 UI에 표시해야 함), statusline 명령의 exit code를 Claude Code가 어떻게 처리하는지, Windows에서 `powershell.exe -EncodedCommand` 경유가 비 ASCII statusline 출력을 깨뜨리지 않는지.
- 2026-09-14 재부팅 확인에서 나온 검은 콘솔 창은 helper를 `WinExe`로 바꿔 해결했다. 실제 설치에서 창과 conhost가 없는 것을 확인했다. helper가 콘솔 출력을 내지 않으므로 실패가 보이지 않아, 설치 스크립트가 태스크 시작 후 snapshot 생성을 확인하고 실패 시 태스크를 되돌린다.
- 종료 시 `CmdRestart`(명령 11)로 패널을 기본 화면에 돌려주는 경로를 추가했고, 실제 장치에서 종료 후 제품 기본화면 복귀를 확인했다. 남은 확인: 복귀 직후 재실행했을 때의 재연결·재동기화.
- 설치 디렉터리 단일화는 앱 포터블 + 센서 단일 루트로 정했다. `setup-sensor-task.ps1 -InstallRoot`가 helper와 snapshot을 한 루트에 놓고, snapshot 검증은 고정 ProgramData 경로 대신 SID 디렉터리 이름·보호 DACL·조상 소유권으로 판정한다. `-AppDirectory`로 앱 payload까지 같은 루트(`<루트>\App`)에 설치하며, `C:\TURZX`로 설치·실행·제거를 확인했다. 설치한 앱이 패널에 H264를 내보내고 같은 루트의 snapshot에서 센서를 읽는다(80개 센서, CPU·GPU 온도). `-Action Remove`는 이제 helper·앱·snapshot 디렉터리와 비게 된 상위를 지우고 PawnIO만 남긴다. 남은 확인: 두 번째 Windows 사용자 계정에서의 읽기와 실행, 첫 설정 UI가 저장하는 `sensor-snapshot`·앱 경로. PawnIO는 커널 드라이버·서비스라 단일 루트에 포함할 수 없다.
- `turzx-control.exe -autostart enable|disable|status`는 GUI 서브시스템 실행 파일이라 셸이 종료를 기다리지 않는다. 설치한 경로에서 `Start-Process -Wait`로 확인하면 Run 값이 정확히 등록·삭제되지만, 스크립트나 UI에서 호출할 때는 완료를 기다려야 한다. 첫 설정 UI가 자동 시작을 다룰 때 이 점을 반영한다.
- 배포물은 설정 없이 실행하면 `display background is required`로 끝난다. 동봉 기본값이 없어서 첫 실행에 `-theme ... -background ... -ffmpeg ... -save-config`가 필요하고 ZIP의 INSTALL.txt에 그렇게 적었다. 첫 설정 UI를 만들 때 실행 파일 옆의 테마·FFmpeg를 기본값으로 잡을지 함께 정한다.
- 2026-09-14 FFmpeg 빌드 중 Symantec이 `msys64\tmp\ffconf.*\test.exe`를 `Heur.AdvML.B`로 격리했다. FFmpeg `configure`가 기능 탐지를 위해 컴파일하는 임시 실행 파일이며 서명 없는 갓 만든 작은 exe라 ML 휴리스틱에 걸린 것이다. 격리가 탐지 결과를 바꿔 조용히 다른 구성으로 빌드될 수 있으므로, 빌드가 끝나면 `bin/ffmpeg-build.json`의 configure와 연기 시험 통과 여부로 확인한다. 근본적으로는 우리가 만든 서명 없는 `ffmpeg.exe`·`turzx-control.exe`도 같은 휴리스틱과 SmartScreen에 걸릴 수 있다 — Authenticode 서명이 배포 전 필수 항목임이 실제로 확인됐다. 빌드 트리 예외 등록은 회사 관리 PC 정책 사항이라 임의로 손대지 않는다.
- 배포 payload는 `bin/app/`에만 둔다. 빌드 스크래치가 payload 안에 있어 ZIP이 348MB로 나온 적이 있어, 패키징이 payload의 하위 디렉터리를 거부한다. ZIP은 56.3MB다.
