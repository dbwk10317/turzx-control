**이 작업이 끝나면 이 문서는 삭제한다.**

남은 검증과 배포 작업:

- 재개 시 `README.md`의 환경으로 빌드한다. 테마 원본은 `assets/backgrounds/`, 빌드·ZIP 출력은 Git에서 제외한 `bin/`을 사용한다. 앱은 정리 작업을 위해 종료했으며 자동 재실행하지 않는다.
- 로컬 정리 시 `.tools/`(개발 도구), `artifacts/sensors-task-20260914/`(고정 해시의 센서 설치 입력), `artifacts/dev-claude-connect-20260911/`(기존 Claude 훅 어댑터)는 재구성·재연결 전까지 보존한다. 이 의존성을 새 빌드·설치 경로로 옮긴 뒤 제거한다.

- Claude 재연결과 실제 재로그인 후 전용 프로필 사용량 수신 복구를 다시 확인한다.
- USB 분리·재연결, 응답 복구, PNG fallback, 지연 및 중단 경로를 측정한다.
- 마지막 프레임까지의 유한 스트림 재생과 안정성·장기 실행 검증을 수행한다. 승인되지 않은 무인 장기 실행은 자동 재시작하지 않는다.
- Windows 실제 설치에서 로그인 자동 시작 enable·disable·status와 재부팅 후 복구를 검증한다.
- standalone ZIP에 실행 파일·FFmpeg·센서 helper·필요 런타임을 포함하고 공식 소스·서명·해시와 깨끗한 Windows PC 설치를 확인한다.
- 첫 설정 UI에서 센서 선택, snapshot 경로 계약, 표준 사용자 동작과 최초 관리자 승인 흐름을 확인한다.
- Windows/macOS/Linux별 빌드·실행·권한·서명 검증을 수행한다.
- 라이선스 notices와 corresponding source 제공 절차를 배포물에 포함한다.
