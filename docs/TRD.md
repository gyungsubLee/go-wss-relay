# 웹·Android WSS 기술 계약

갱신일: 2026-10-22. 상태: 단일 노드·공용 TS·분산 경로 구현. 실기기와 운영 검증은 진행 감사로 구분한다.
모듈: github.com/gyungsubLee/go-wss-relay.

## 소유권

| 경로 | 책임 |
|---|---|
| api/relay/v2/relay.proto | WSS 바이너리 계약의 단일 원본 |
| gen/go/relay/v2 | 생성한 Go 메시지 |
| client/src/relay/gen | 같은 원본에서 생성한 TypeScript 메시지 |
| internal/relayv2 | 노드 방·접속권·세대·캐시·전송 예산·계측 |
| internal/coordv2 | 노드 배정·예약·등록·장애 감지·복원·드레이닝 |
| internal/controlv2 | 공개 인증·로비·FIFO·개인 배정 응답 |
| client/src/relay | 게임에 독립적인 연결·제어 API SDK |
| client/src/demo | 좌표 본문·모션·보간·검증 UI |
| client/MinbWebKit | 고정한 공용 viewport·SR·빌드 정책 |
| cmd/relay-load | 별도 TLS 서버의 부하 프로필 측정 |
| cmd/wire-check | Go→TS→Go 메시지 호환 검사 |

기존 v1·UDP·Unity 코드는 이력과 회귀 범위로 남아 있다. 새 기본 실행 명령은 cmd/wss-relay다.

## 전송과 권한

운영 제어 API는 HTTPS, 게임 경로는 /v2/relay WSS이며 서브프로토콜은 relay.v2다.
관리 API는 운영자 토큰으로 인증하며 루프백 주소에서 수신한다.
클라이언트에는 플레이어 토큰과 자기 접속권만 전달한다. 운영자 토큰은 전달하지 않는다.
최초 Connect는 번호 0, 나머지는 증가하는 uint32 번호를 사용한다.
uint64는 TS에서 bigint로 보존한다. 모든 메시지 종류의 양방향 호환 검사를 유지한다.

전체 메시지·본문·빈도·TTL·큐 한도는 [메시지 명세](specs/2026-10-20-relay-v2-message-spec.md)를 따른다.
연결별 제어·사건 대기열은 32개·256 KiB, 참가자별 대기 상태는 한 개다.
방 잠금 안에서 네트워크 쓰기를 하지 않는다. 실제 소켓 송신은 연결별 writer 하나가 수행한다.
스냅샷 이전 상태가 이후에 객체를 복원하지 않도록 세대·상태 번호와 참가자 목록을 검사한다.

## 공개 제어 API

접두사는 /v2/api다. Authorization은 Bearer 플레이어 토큰이다.
CORS preflight를 인증 전에 처리한다. WSS Origin 검사는 별도로 수행한다.
게스트 발급에는 IP별 제한을 적용한다. 임의 Forwarded 헤더로 접속자 IP를 덮어쓰지 않는다.

| 요청 | 역할 |
|---|---|
| POST /sessions | 서버 식별자와 15분 플레이어 토큰 발급 |
| POST /sessions/refresh | 현재 플레이어 토큰 갱신 |
| GET /lobbies | 호환 계약별 공개 로비 목록 |
| GET /lobbies/{id}/preview | 코드로 찾은 로비의 참가 전 요약 |
| POST /lobbies | 방 생성 |
| GET /lobbies/{id} | 참여한 방 조회 |
| POST /lobbies/{id}/join | 방 코드로 참가 |
| POST /lobbies/{id}/ready | 준비 상태와 revision 갱신 |
| POST /lobbies/{id}/leave | 대기방 퇴장·소유자 이전 |
| POST /lobbies/{id}/start | 소유자가 매치 시작 |
| GET /lobbies/{id}/assignment | 자기 배정만 조회 |
| POST /lobbies/{id}/assignment | 같은 참가자의 접속권 재발급 |
| POST /matchmaking | 호환 계약·정원별 FIFO 대기 등록 |
| GET /matchmaking/{id} | 자기 매칭표 조회 |
| DELETE /matchmaking/{id} | 아직 묶이지 않은 대기 취소 |

참가·준비·시작·퇴장은 현재 revision을 요구한다. 변경 충돌은 409다.
예약 결과가 불확실하면 참가자 구성을 고정하고 같은 시작 요청을 재시도한다.
전체 본문·응답 계약은 [HTTP API 명세](specs/2026-10-22-control-v2-http.md)를 따른다.

## 측정과 분산

관리 경로 GET /v2/status는 누적 전송량, 큐·쓰기 지연, 대기 시간, Go 메모리와 연결 수를 반환한다.
Protobuf 바이트는 TLS/TCP 오버헤드를 포함하지 않는다. 히스토그램 p95/p99는 버킷 상한이다.
부하 도구는 별도 프로세스의 OS CPU·RSS를 수집한다. 실제 Android 지연과 구분한다.

분산은 같은 코드베이스의 standalone/coordinator/relay 역할로 구성한다.
실제 프로세스 검사는 방 단일 소유·격리, 드레이닝 중 복귀, 배정기 재시작, 노드 종료·재시작, 증설과 배수 완료 후 종료를 확인한다.
운영자는 [v2 배포 절차](../../infrastructure/v2/README.md)의 네트워크·키·상한을 환경에 맞게 설정한다.
기존 UDP 배포 스크립트는 v2 배포 절차가 아니다.
