# 원래 계획 1~5단계 완료 감사

판정일: 2026-10-22. 전체 목표는 진행 중이다. 구현과 실행 증거가 없는 조건을 완료로 표시하지 않는다.
기준은 docs/plans/2026-10-20-web-android-wss-relay-refactor.md의 원래 범위와 완료 기준이다.

## 단계 판정

| 단계 | 확인한 증거 | 판정과 남은 조건 |
|---|---|---|
| 1. 계약 전환 | api/relay/v2, 메시지·HTTP 명세, PRD·TRD, 17종 Go→TS→Go 실행 | 구현·호환 검사 확인. 아래 세부 누락은 남은 조건으로 추적 |
| 2. 단일 WSS 중계 | 실제 Chrome 두 창, SDK 통합, Go 방 격리·권한·큐·TTL 검사 | 로비·준비·매칭·좌표·사건 경로 확인 |
| 3. SDK·복구 | 공용 SDK, 세대·유예·재발급 검사, Android 디버그 빌드 | 진행 중. 브라우저↔Android 실기기 교환·기기 수명주기·망 전환 미검증 |
| 4. 전송량 검증 | TLS 18개 프로필·느린 수신·재접속 보고서, 별도 OS 자원 계측 | 서버 프로필 재측정·뱀 크기·TCP 지연·연결 손실 확인. 저속 실제 장치·모바일 망 조건은 미검증 |
| 5. 노드 분산 | 실제 배정기·노드 프로세스 6개 시나리오, 예약 응답 유실 회귀 검사 | 로컬 분산 실행과 Caddy 파서·모듈 검사 확인. 실제 운영 호스트 배포는 미실행 |

## 요구사항별 근거

| 요구사항 | 근거 또는 상태 |
|---|---|
| 웹·Android 공용 바이너리와 uint64 보존 | client/src/relay/gen, check-wire.ts, 17개 본문 왕복과 최댓값 검사 통과 |
| MinbWebLib 참조 | MinbWebKit 4.0 ee2715f 서브모듈, 실제 viewport·디버거·빌드 프로필·native Activity 사용 |
| 새 저장소·원본 보존 | gyungsubLee/go-wss-relay, codex/wss-v2. 원본 소스 수정 없음 |
| 공개·비공개·준비·버전·소유자 이전 | controlv2/http_test.go의 로비 계약 검사 통과 |
| FIFO·개인 배정·권한 격리 | controlv2 검사, check-sdk.ts, 브라우저 화면 증거 |
| 플레이어 토큰과 운영자·접속권 분리 | playerauth·controlv2·relayv2·관리 API 인증 검사 |
| Origin·CORS·신뢰 프록시·IP 제한 | httpx/ip_test.go, 제어 API·WSS Origin 검사 |
| 한 참가자당 최신 대기 상태 | relayv2 상태 병합 검사, check-sdk.ts의 100회 발표 병합 검사 |
| 사건 접수·순서·유계 큐 | relayv2와 SDK 검사. 상대 적용 완료·단절 이후 사건 재전송은 계약에 없음 |
| 스냅샷과 이후 상태 순서 | TestPendingSnapshotIsNotOvertakenByState, SDK 세대·상태 번호 검사 |
| 이중 접속·유예·재발급·방 만료 | relayv2 credential expiry·replacement·reservation expiry 검사 |
| 가득 찬 노드에서 세대 교체 | TestFullParticipantCapacityStillAllowsConnectionReplacement 통과 |
| 비정상·느린 연결 격리 | 바이트 큐 상한·차단 소켓 쓰기 검사. OS 버퍼 축소만으로 지연을 단정하지 않음 |
| 서버·클라이언트 종료 코드 | terminal code 검사와 SDK 오류별 복구 처리 |
| 웹 중단·복귀 | 실제 SDK suspend/resume 검사. 실제 Android 알림 수신은 별도 |
| APK·AAB 구조·내장 웹·서명 | Android 산출물 JSON. 디버그 변형이며 Play 출시 키·업로드는 범위에 없음 |
| Android 실제 Origin·WebView | 코드상 https://localhost 설정. 실기기 관찰 전 |
| Android 화면 잠금·복귀·Wi-Fi/이동통신 | USB 디버깅 기기 없음. 실행하지 않음 |
| 100·500·1000명, 방 2·8·16명, 10·20 Hz | load/profile.json과 18개 보고서. 최신 연결·IP 정책의 재측정 통과. load/README.md 참고 |
| 느린 수신자·재접속 집중 | load/slow-*.json, reconnect-*.json. 기본 IP 정책 재측정 통과. 1000명 최장 약 5.8초 |
| 실제 뱀 전체 상태·초기 전송 크기 | snake-demo.v1 실제 본문·32/64개 경로·16명 스냅샷 측정과 실제 SDK 수신 통과. JSON 수치에 따라 바이트 변동 |
| 저속 실제 장치·망 지연·손실 | TCP 방향별 100 ms·연결 손실·g2 복귀 확인. 저속 장치·무선 IP 손실률은 미검증 |
| 내부 큐·쓰기 p95/p99·최장 대기 | 관리 Stats 계측. 히스토그램은 버킷 상한 |
| 바이트·CPU·RSS·GC·복구 시간 | relay-load 별도 프로세스와 Node SDK 보고서. Go CPU 추정과 OS CPU를 구분 |
| 전송 예산·입장 제어 | TrafficPolicy, --traffic-config, 노드 건강·예약 슬롯·송신 압력·초기 CPU 보수 제한 |
| 방 단일 노드 소유·멱등 예약 | coordv2 회귀 검사, 실제 프로세스 격리 검사 |
| 예약 성공 후 응답 유실 | TestLostReservationResponseNeverMovesRoom, 제어 API 참가자 구성 고정 검사 |
| 증설·등록·드레이닝 중 복귀 | check-cluster.ts의 실제 프로세스 시나리오 통과 |
| 노드 장애·배정기 재시작·방 복원 | check-cluster.ts. 노드 재시작 시 기존 방으로 위장 복구하지 않음 |
| 소유권 충돌 시 신규 배정 차단 | TestConflictingOwnershipBlocksPlacementUntilResolved 통과 |
| Caddy·systemd·분산 설정 | infrastructure/v2 추가. 공식 Caddy 2.11.7 adapt·validate 통과. 대상 Linux 서비스 기동·방화벽은 미실행 |
| 기존 UDP 기본 경로 제거 | 기본 Make 빌드는 wss-relay. v1·UDP 소스는 이력·회귀 범위로 보존 |
| 공용 상태 권한자 영구 이탈 시 경기 종료 | HTTPS 로비 소유자로 고정. 비권한자 거부·복귀 유지·종료 상태·영구 이탈 검사와 실제 Chrome 종료 확인 |
| Go·TS 실행 의존성 보안 검사 | Go 1.26.10 적용 후 govulncheck 발견 항목 없음, npm 실행 의존성 0건 |
| 빠른 매칭의 최초 접속 순서 | 상대가 먼저 WSS에 연결해도 대기. 실제 두 Chrome과 check-snake.ts 회귀 검사 통과 |
| CI | Go·웹·호환·실제 프로세스 검사 정의 추가. 현재 포크 default main에는 워크플로가 없어 원격 실행 미등록. 로컬 동등 검사는 통과 |

## 남은 검증

USB 실기기가 없어 브라우저↔Android 교환·잠금·앱 복귀·Wi-Fi/이동통신 전환을 실행하지 못했다.
저속 실제 장치·무선 패킷 손실의 증거도 없다. TCP 프록시 결과로 대체하지 않는다.
대상 Linux의 systemd 기동·DNS·방화벽·공개 인증서는 실제 배포 시 확인한다. 운영 배포 완료를 주장하지 않는다.
원격 CI는 기본 브랜치에 워크플로가 등록된 뒤 확인할 수 있다. 초안 PR을 임의 병합하지 않았다.
전체 목표는 Android·장치·망 조건의 증거를 확인할 때까지 완료로 표시하지 않는다.
