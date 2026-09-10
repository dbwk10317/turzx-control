# turzx-control

TURZX / Turing USB 스마트 스크린 패널에 Claude·Codex 사용량과 시스템 센서를 띄우는
크로스플랫폼 데몬. Windows·macOS·Linux에서 동작하도록 만든다.

**현재 상태: 구현 전 리뷰를 반영한 설계 단계.** 패킷 생성 검증 코드만 있으며 제품 구현은
시작하지 않았다. 실시간 스트리밍과 Windows 운용 조건은 추가 검증이 필요하다.
평가·라이브러리 선택·Go 구현 원칙·빌드/서명/배포 계획은 [`DESIGN.md`](DESIGN.md)에 있다.

## 무엇이 확인되었나

- TURZX 9.2"(VID `0x1cbe` / PID `0x0092`, 462×1920)에서 화면 출력과 H264 비디오 재생 확인
- sync·밝기·PNG/JPEG 헤더를 Go로 포팅하고 업스트림 기준 패킷과 바이트 단위 일치를 검증
- macOS에서 낱장 PNG 전송과 파일 H264 재생·인코딩의 짧은 실측 기록 확보 (`DESIGN.md` 2~3절)

사무실에서는 Windows 데몬이 USB와 시스템 센서를 담당하고, Windows/WSL에서 실행한
Claude·Codex의 사용량을 공급자별로 수집한다. 기본 지표는 gopsutil, Windows 온도·GPU는
LibreHardwareMonitor, 영상은 FFmpeg를 활용하는 방향이다. 다음 단계와 통과 기준은
`DESIGN.md` 8절에 있다.

## 라이선스

GPL-3.0-or-later.

이 프로젝트의 장치 프로토콜 구현은
[mathoudebine/turing-smart-screen-python](https://github.com/mathoudebine/turing-smart-screen-python)
(Copyright © 2021 Matthieu Houdebine, GPL-3.0-or-later)의 구현을 참조해 Go로 옮긴 것이다.
`testdata/golden.json`의 기준 패킷도 해당 구현으로 생성했다. 원 저작자와 기여자들에게 감사한다.
