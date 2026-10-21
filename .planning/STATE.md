# WSS 전환 작업 상태

목표: MinbWebLib 참조로 TS 클라이언트를 만들고 원래 계획 1~5단계까지 구현·검증한다.
저장소: go-wss-relay. 브랜치: codex/wss-v2. 전체 목표는 진행 중이다.

## 확인한 진행

- v2 계약·공용 TS SDK·MinbWebKit 검증 화면·Android 빌드 구성 구현.
- 공개 인증·갱신·로비 ready/revision·공개 목록·퇴장·FIFO·접속권 재발급 구현.
- 최신 Chrome 두 창의 준비·시작·좌표·양방향 사건·g2 복귀 확인.
- Go 1.26.10 보안 패치·취약점 검사, 전체 Go 경합·정적 검사, TS 빌드, 실제 SDK 통합과 17종 바이너리 호환 검사 통과.
- 실제 배정기·노드 프로세스의 단일 소유·격리·증설·드레이닝·장애·재시작 검사 통과.
- 노드·방 바이트 예산·입장 제어·계측·CLI 설정·v2 배포 템플릿 추가.
- Android 최신 디버그 APK·AAB 빌드 성공. USB 기기는 연결되지 않음.
- 최신 IP 정책의 TLS 18개 프로필·실제 뱀 크기 2개 프로필·느린 수신·1000명 복귀 통과.
- 뱀 전체 몸통·공용 상태 권한자·종료와 TCP 지연·연결 손실 검사 통과.
- 빠른 매칭에서 상대가 먼저 연결되는 시작 순서 오류 수정·실제 두 Chrome 재검증 통과.

- 웹·앱 교차 검증용 standalone 공유 API/WSS 설정과 실기기 절차 추가. Caddy adapt·validate 통과.

## 남은 필수 작업

완료 감사는 docs/evidence/v2/completion-audit.md를 따른다.
Android 실기기·저속 장치·무선 망 조건의 증거가 필요하다.
Caddy adapt·validate는 통과했다. 대상 Linux 운영 배포와 원격 CI 실행은 확인하지 않았다.
기존 UDP 프로젝트의 GSD 완료 표시는 새 WSS 5단계의 완료 증거가 아니다.
