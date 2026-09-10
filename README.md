# turzx-control

TURZX / Turing USB 스마트 스크린 패널에 Claude·Codex 사용량과 시스템 센서를 띄우는
크로스플랫폼 데몬. Windows·macOS·Linux에서 동작하도록 만든다.

**현재 상태: 설계 단계.** 장치 실측과 설계가 끝났고 구현은 시작하지 않았다.
설계와 실측 근거는 [`DESIGN.md`](DESIGN.md)에 있다.

## 무엇이 확인되었나

- TURZX 9.2"(VID `0x1cbe` / PID `0x0092`, 462×1920)에서 화면 출력과 H264 비디오 재생 확인
- 벤더 프로토콜을 Go로 포팅하고, 업스트림 구현이 만든 패킷과 바이트 단위 일치를 검증
- 낱장 PNG 경로와 H264 스트림 경로의 처리량을 실측 (`DESIGN.md` 3절)

## 라이선스

GPL-3.0-or-later.

이 프로젝트의 장치 프로토콜 구현은
[mathoudebine/turing-smart-screen-python](https://github.com/mathoudebine/turing-smart-screen-python)
(Copyright © 2021 Matthieu Houdebine, GPL-3.0-or-later)의 구현을 참조해 Go로 옮긴 것이다.
`testdata/golden.json`의 기준 패킷도 해당 구현으로 생성했다. 원 저작자와 기여자들에게 감사한다.
