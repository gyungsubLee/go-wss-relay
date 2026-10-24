# 웹·Android WSS 기술 계약

갱신일: 2026-10-23. v2 전용 구조와 로컬 검사를 마쳤다. Task 10 독립 리뷰와 전체 브랜치 리뷰를 마쳤다. 최종 I1 수정도 재리뷰에서 닫았다.
실기기와 운영 검증은 [완료 감사](evidence/v2/completion-audit.md)로 구분한다.
모듈: github.com/gyungsubLee/go-wss-relay.

## 최종 폴더 구조

아래는 `dfcbdd3` 구현의 주요 경로와 Go 구현 파일이다. 테스트 파일과 생성물의 세부 파일은 생략했다.
파일 분리는 같은 패키지 안에서 책임을 나눈다. 구현을 변경할 때는 [승인 설계](superpowers/specs/2026-10-23-go-structure-refactor-design.md)의 경계를 따른다.

```text
go-wss-relay/
├── cmd/
│   ├── wss-relay/main.go
│   ├── relay-load/main.go
│   ├── wire-check/main.go
│   └── dev-tls/main.go
├── internal/
│   ├── app/
│   │   ├── config.go
│   │   └── run.go
│   ├── control/
│   │   ├── http.go
│   │   ├── manager.go
│   │   ├── lobby.go
│   │   ├── queue.go
│   │   ├── allocation.go
│   │   ├── views.go
│   │   └── errors.go
│   ├── coordinator/
│   │   ├── config.go
│   │   ├── node_client.go
│   │   ├── placement.go
│   │   ├── recovery.go
│   │   └── management.go
│   ├── relay/
│   │   ├── server.go
│   │   ├── connection.go
│   │   ├── rooms.go
│   │   ├── credentials.go
│   │   ├── reservation.go
│   │   ├── management.go
│   │   └── metrics.go
│   ├── playerauth/auth.go
│   ├── operatortoken/token.go
│   ├── protocol/
│   │   ├── codec.go
│   │   └── id.go
│   └── httpx/
│       ├── httpx.go
│       └── ip.go
├── api/relay/v2/
│   ├── relay.proto
│   └── relay-baseline.binpb
├── gen/go/relay/v2/relay.pb.go
├── client/
│   ├── src/relay/
│   │   ├── api.ts
│   │   ├── client.ts
│   │   └── gen/relay/v2/relay_pb.ts
│   ├── src/demo/
│   ├── scripts/
│   ├── android/
│   └── MinbWebKit/
├── infrastructure/
│   ├── config/
│   ├── ops/
│   ├── scripts/
│   │   └── tests/
│   └── docs/operations.md
├── scripts/
├── docs/
├── .planning/
├── .github/workflows/ci.yml
├── Makefile
├── buf.yaml
├── buf.gen.yaml
├── go.mod
└── go.sum
```

## 패키지 역할과 의존 관계

의존 열은 일반 Go 소스의 직접 import 중 저장소 내부 경로만 적었다. 표준 라이브러리와 외부 모듈은 제외했다.
`internal/` 접두사는 패키지 이름에서 생략했다. `gen`은 `gen/go/relay/v2`를 뜻한다.

| 패키지 | 역할과 상태 소유권 | 직접 의존 |
|---|---|---|
| `app` | 설정 검증, 의존성 조립, listener 생성·실행·종료와 자원 정리 | `control`, `coordinator`, `relay`, `playerauth`, `operatortoken`, `httpx` |
| `control` | `Server`의 HTTP 입력·인증·응답, `Manager`의 로비·FIFO·소속·만료·배정 상태와 잠금 | `relay`, `playerauth`, `protocol`, `httpx` |
| `coordinator` | 노드 등록·선택, 예약 소유권, 장애 감지·복원·드레이닝 | `control`, `relay`, `protocol`, `operatortoken`, `httpx` |
| `relay` | 방·연결·접속권·재접속 세대·상태 캐시·이벤트 큐·전송 예산·계측 | `protocol`, `httpx`, `gen` |
| `playerauth` | 플레이어 토큰 발급·서명·만료 검증 | `protocol` |
| `operatortoken` | 토큰 파싱과 파일 권한·크기·교체 여부 검증 | 없음 |
| `protocol` | Protobuf 인코딩·디코딩, 메시지와 식별자 검증 | `gen` |
| `httpx` | HTTP 입력·오류·요청 제한·신뢰 프록시 IP 처리 | 없음 |
| `gen/go/relay/v2` | 스키마에서 생성한 메시지 타입 | 없음 |

`control/http.go`는 Manager를 호출하고 응답을 쓴다. `manager.go`·`lobby.go`·`queue.go`·`allocation.go`는 상태 변경을 처리한다.
`views.go`는 응답용 복사본을 만든다. `errors.go`는 업무 오류 타입을 정의하고 `http.go`가 HTTP 상태로 변환한다.
Manager는 잠금 안에서 응답 슬라이스와 맵을 복사한다. JSON 인코딩과 원격 예약 호출은 잠금 밖에서 실행한다.

`coordinator`는 노드 호출·배정·복구·관리 API를 파일별로 나눈다. `Pool`은 노드와 예약 소유권을 보관한다.
`relay`는 서버·연결·방·접속권·예약·관리·계측을 파일별로 나눈다. 잠금 순서는 `server.mu → room.mu → client.mu`다.
메시지는 연결별 writer 하나가 전송한다. 기능 모듈의 생성과 정리는 `app.Run`이 조립한다.

### 명령과 Go 외부 경로

| 경로 | 역할 | 구현 또는 계약 |
|---|---|---|
| `cmd/wss-relay` | 신호와 종료 코드를 처리하는 서버 진입점 | `app.Run` 호출 |
| `cmd/relay-load` | 별도 TLS 서버의 부하 측정 | `relay`와 Go 생성 메시지 사용 |
| `cmd/wire-check` | Go→TS→Go 메시지 호환 검사 | Go 생성 메시지 사용 |
| `cmd/dev-tls` | 로컬 인증서 생성 | 표준 라이브러리 사용 |
| `api/relay/v2` | WSS 바이너리 계약과 호환성 기준 | `relay.proto`, `relay-baseline.binpb` |
| `client/src/relay` | 게임에 독립적인 공용 TS SDK | `api.ts`는 HTTP, `client.ts`는 WSS 담당 |
| `client/src/relay/gen/relay/v2` | 같은 스키마에서 생성한 TS 메시지 | SDK가 사용 |
| `client/src/demo` | 게임 상태·모션·보간·검증 화면 | SDK와 `snake-state.ts`의 게임 계약 사용 |
| `client/android` | Capacitor Android 앱 | 웹 SDK와 검증 화면 사용 |
| `client/MinbWebKit` | 고정한 공용 viewport·SR·빌드 정책 | 외부 서브모듈 |
| `client/scripts` | SDK·수명주기·복구·호환 검사 | 실제 소켓 검사와 경계 대체 검사를 구분 |
| `infrastructure` | 역할별 설정·서비스·패키징·배포·비용 가드 | `config`, `ops`, `scripts`, `docs` |
| `scripts` | 도구 설치·프로토콜 생성·부하 결과 검사 | Makefile과 CI에서 사용 |

## 역할별 실행 조립

[app.Run](../internal/app/run.go)이 `--role` 값으로 구성을 선택한다. 세 역할은 같은 `cmd/wss-relay` 바이너리를 사용한다.

- `standalone`: `control.Server`와 `relay.Server`를 함께 만든다. `control.LocalAllocator`가 같은 프로세스의 방 예약과 접속권 갱신을 호출한다.
- `coordinator`: `control.Server`와 `coordinator.Pool`을 만든다. Pool이 원격 relay의 관리 API로 예약·조회·복원을 처리한다.
- `relay`: `relay.Server`가 WSS 연결과 노드 관리 API를 제공한다. 공개 로비 API는 coordinator가 제공한다.

`control`이 [Allocator](../internal/control/allocation.go) 계약을 소유한다. `coordinator.Pool`과 `control.LocalAllocator`가 이 계약을 구현하며 `app`이 선택해 주입한다.
실제 import 방향은 `coordinator → control`이다. `control → relay`는 LocalAllocator와 `relay.Ticket`·오류 타입을 공유하는 의존이다.
`protocol`·`httpx`·`operatortoken`은 실행 역할을 선택하지 않는다.

참가자는 제어 API에서 자기 배정을 받은 뒤 지정된 relay와 WSS로 직접 데이터를 교환한다.
coordinator는 게임 데이터 중계 경로에 들어가지 않는다. 두 경로의 조립 근거는 [app.Run](../internal/app/run.go)과 [노드 호출](../internal/coordinator/node_client.go)에 있다.
`app.Run`은 취소·오류·드레이닝 때 listener와 릴레이를 닫는다. Pool이 소유한 유휴 HTTP 연결도 정리한다.

## 승인 설계와 구현의 차이

[승인 설계](superpowers/specs/2026-10-23-go-structure-refactor-design.md)의 큰 폴더 구조와 상태 소유권은 그대로다.
아래는 구체 구현 선택, 작업 순서 조정과 리뷰 후 보강을 구분한 기록이다.

| 항목 | 구분 | 실제 구현과 이유 | 소스 근거 |
|---|---|---|---|
| Manager 연결 | 구현 선택 | HTTP `Server`가 구체 타입 `*Manager`를 임베딩한다. 상태 소유권을 분리하면서 전달 메서드와 새 인터페이스를 줄였다. | [http.go](../internal/control/http.go) |
| v1 인증 옵션 제거 | 작업 순서 조정 | 실행 조립을 바꾼 Task 5에서 임의 키 옵션을 제거했다. 전체 제품 호출자가 안정 키를 사용함을 확인하고 활성 v2 토큰 결과를 유지했다. | [auth.go](../internal/playerauth/auth.go), [실행 계획](superpowers/plans/2026-10-23-go-structure-refactor.md) |
| 배포 입력 검사 | 리뷰 후 보강 | `caFile` 타입·숫자 표기·원시 URL 검사를 보강했다. Python과 Go의 해석 차이로 잘못된 설정이 승격하는 사례를 막았다. | [deploy-preflight.py](../infrastructure/scripts/deploy-preflight.py) |
| 데모 중복 진입 | 추가 리뷰 수정 | 공통 `entryAction`과 회귀 검사를 추가했다. 겹친 create·join·match가 먼저 성공한 서버 소속을 버리는 오류를 수정했다. | [main.ts](../client/src/demo/main.ts), [회귀 검사](../client/scripts/check-demo-entry.mjs) |

배정 결과 분류·FIFO 만료·토큰 파일 보안·연결 정리·SDK 수명주기 검사는 처음부터 승인 범위였다.
위 보강은 서버 API나 패키지 경계를 새로 만들지 않았다. 발견 조건·수정 커밋·실행 결과는 [구조 변경 증거](evidence/v2/2026-10-23-structure-refactor.md)에 보존한다.

v1 서버·UDP·C#·Unity 실행 경로는 제거했다. 이전 구현은 Git 이력에서 확인한다.
활성 인증의 `tokenVersion=1`·`player-token/v1`과 `snake-demo.v1`·`demo.v1`·`load.v1`은 유지한다.

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
운영자는 [배포 절차](../infrastructure/README.md)의 네트워크·키·상한을 환경에 맞게 설정한다.
배포는 flat `infrastructure/{config,ops,scripts,docs}`만 사용한다. 운영 형식과 수동 호스트 준비는 [운영 절차](../infrastructure/docs/operations.md)를 따른다.
검사 명령·소스 연결·미실행 조건은 [구조 변경 증거](evidence/v2/2026-10-23-structure-refactor.md)에 있다.
