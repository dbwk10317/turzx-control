**이 작업이 끝나면 이 문서는 삭제한다.**

남은 작업을 막고 있는 것에 따라 나눴다. 완료된 설계와 검증은 여기 남기지 않고
`README.md`·`AGENTS.md`에 반영했다.

## 1. 코드 구멍

- 첫 설정 UI에 센서·표시 설정이 없다. 현재 설정 UI는 `/api/state`와 Codex·Claude
  로그인/로그아웃뿐이고, 센서 선택·테마 선택·설치 실행은 전부 CLI 플래그와
  `settings.json` 직접 편집이다. 필요한 것: helper snapshot에서 CPU·GPU·RAM 온도
  후보 제시와 선택 저장, 센서 활성화 시 `setup-sensor-task.ps1` 실행과 결과 표시
  (취소·실패·재부팅 필요 구분), 설치 루트 입력, 자동 시작 토글. 결정이 필요한
  것: 앱이 설치 스크립트를 직접 실행해 UAC를 띄울지, UI가 명령줄을 제시하고
  사용자가 실행할지.
- 배포물은 설정 없이 실행하면 `display background is required`로 끝난다. 동봉
  기본값이 없어서다. 실행 파일 옆의 테마·FFmpeg를 기본값으로 잡거나 첫 설정
  UI가 받게 한다. ZIP의 INSTALL.txt는 현재 동작대로 첫 실행 명령을 적어 두었다.
- Claude 사용량은 이제 사용자의 실제 프로필에 hook을 설치해 별도 조작 없이
  들어온다. 남은 확인: 실제 Claude Code 세션에서 수신·표시, 그리고 계정을 바꿨을 때
  자동 재바인딩과 이력 초기화. 조직이 관리 설정에 `allowManagedHooksOnly`를 켜면
  사용자 statusline이 경고 없이 사라지므로, UI가 "설치됨 · 수신 없음"을 구분해
  보여줘야 한다.
- `turzx-control.exe -autostart enable|disable|status`는 GUI 서브시스템이라 셸이
  종료를 기다리지 않는다. Run 값은 정확히 등록·삭제되지만, 스크립트나 UI에서
  호출할 때는 완료를 기다려야 한다. 첫 설정 UI가 자동 시작을 다룰 때 반영한다.

## 2. 물리적 조작이 필요한 검증

장치가 연결된 상태에서 한 번에 묶어서 하는 편이 효율적이다.

- USB 분리·재연결: 재연결 backoff(1~30초), IN 큐 drain, sync 복구.
- 영상 실패 시 PNG fallback: 1·2·4초 간격 3회 재시도, 정상 영상 30분 후 예산
  초기화.
- 마지막 프레임까지의 유한 스트림 재생과 중단 경로, 지연 측정.
- 로그인 자동 시작을 켠 뒤 재부팅했을 때의 복구.
- 안정성·장기 실행. `scripts/measure-stream.ps1`로 계측하며 `turzx-probe`가
  필요하다. 승인되지 않은 무인 장기 실행은 자동 재시작하지 않는다.

## 3. 계정이 필요한 검증

- 두 번째 Windows 사용자 계정에서 snapshot 읽기(`BUILTIN\Users` 읽기 ACE)와
  설치한 앱 실행. 예약 작업은 SID별이라 계정마다 설치가 한 번씩 필요하다.
- Claude 재연결과 실제 재로그인 후 전용 프로필 사용량 수신 복구. 같은 자리에서
  다음 세 가지도 확인한다: `claude auth login --claudeai`가 headless로 URL을
  출력하는지(출력하면 UI에 표시해야 함), statusline 명령의 exit code를 Claude
  Code가 어떻게 처리하는지, Windows에서 `powershell.exe -EncodedCommand` 경유가
  비 ASCII statusline 출력을 깨뜨리지 않는지.

## 4. 결정·구매가 필요한 것

- Authenticode 서명은 개인 프로젝트라 자체 서명으로 간다. `scripts/sign-artifacts.ps1`로
  네 실행 파일에 서명하고 타임스탬프까지 넣었다. 자체 서명은 공개 신뢰가 아니라
  SmartScreen 경고와 휴리스틱 오탐을 없애지 못한다. 남은 결정: 실행할 PC의 신뢰
  저장소에 인증서를 설치할지(설치하면 그 PC에서는 "알 수 없는 게시자"가 사라진다),
  인증서 개인 키를 어디에 보관할지. 외부 배포를 하게 되면 공개 신뢰 인증서로
  바꾸고 `-Thumbprint`만 교체한다.
- GPL corresponding source 제공 경로. `package-zip.ps1`의 `-SourceOffer`가 아직
  자리표시자다. FFmpeg `bf1b838f2a`와 x264 `b35605ac`의 소스 아카이브를 올릴
  위치를 정해야 한다. `scripts/build-ffmpeg.sh`는 이미 저장소에 있다.
- `assets/backgrounds/smon-halloween.mp4`는 2026-09-15에 `git filter-branch`로 전체
  이력에서 지우고 force push했다. 새로 clone하면 36개 커밋이 그대로 있고 파일은
  어디에도 없다(9.1MB). 커밋 해시는 전부 바뀌었으므로 다른 clone이 있으면 다시
  받아야 한다. **남은 문제**: 저장소가 PUBLIC이고, force push 후에도 옛 커밋
  `3326eb99`가 GitHub에서 해시로 접근되며 그 커밋의 파일이 그대로 내려받힌다
  (blob `331fee1c`, 8345644바이트). 참조되지 않는 객체를 실제로 지우려면 GitHub
  Support에 정리를 요청해야 한다(fork 0개라 요청 조건은 유리하다). 공개 상태였던
  기간에 이미 받아간 사본은 되돌릴 수 없다. `smon-halloween.theme.json`은 배경
  영상이 아닌 레이아웃·팔레트 데이터라 그대로 추적한다.
- macOS·Linux 배포 범위. 현재는 Windows 전용 데몬이다. 실제 배포 계획이 있어야
  빌드·실행·권한·서명 검증의 크기가 정해진다.

## 5. 마지막 확인

- 개발 도구가 없는 깨끗한 Windows에서 ZIP을 풀어 실행·설치. .NET 미설치 상태의
  helper 동작, UAC 흐름, 표준 사용자 계정 동작, 서명 전이면 SmartScreen 경고가
  여기서 실제로 드러난다.
- `artifacts/sensors-task-20260914/`는 이전 고정 해시의 입력이라 현재 설치에
  쓰이지 않는다. `-owner` publish로 대체됐으므로 정리해도 된다.
  `artifacts/dev-claude-connect-20260911/`(기존 Claude 훅 어댑터)는 재연결 검증
  전까지 보존한다.
- `internal/claude`의 `TestConcurrentWriters`가 이 PC에서 자주 실패한다. 8개 writer가
  250ms 락 타임아웃을 두고 경합하는 합성 테스트인데, 파일 조작마다 개입하는 안티바이러스
  때문에 250ms가 빠듯하다. 2026-09-15에 이번 세션 이전 코드(`02d15ff`)로 되돌려 같은
  실패를 재현해 회귀가 아님을 확인했다. 실제로는 세션마다 파일이 분리되고 Claude Code가
  statusline 실행을 debounce·취소하므로 경합이 거의 없고, 놓친 쓰기는 다음 렌더에서
  복구된다. 제품 상수를 테스트 때문에 바꾸지 않았다. 다른 PC에서 재현되는지 확인한다.
