# v2 전용 Go 서버 리팩터링 설계

작성일: 2026-10-23. 기준 커밋: `ba3db51`.
상태: 사용자가 2026-10-23에 v1 제거와 v2 전용 구조 구현을 승인했다.
승인 범위는 현재 브랜치의 구현·회귀 검사·로컬 커밋이다. push·merge·배포는 포함하지 않는다.

## 목표 폴더 구조

```text
go-wss-relay/
├── cmd/
│   ├── wss-relay/main.go          # 유일한 서버 실행 명령
│   ├── relay-load/main.go         # WSS 부하 측정
│   ├── wire-check/main.go         # Go·TS wire 왕복 검사
│   └── dev-tls/main.go            # 로컬 TLS 검사 도구
├── internal/
│   ├── app/                      # 설정·조립·실행·종료
│   │   ├── config.go
│   │   └── run.go
│   ├── control/                  # 공개 제어 API와 매칭 상태
│   │   ├── http.go
│   │   ├── manager.go
│   │   ├── lobby.go
│   │   ├── queue.go
│   │   ├── allocation.go
│   │   ├── views.go
│   │   └── errors.go
│   ├── coordinator/              # 노드 선택·예약·복구
│   │   ├── config.go
│   │   ├── node_client.go
│   │   ├── placement.go
│   │   ├── recovery.go
│   │   └── management.go
│   ├── relay/                    # 방·WSS 연결·접속권
│   │   ├── server.go
│   │   ├── connection.go
│   │   ├── rooms.go
│   │   ├── credentials.go
│   │   ├── reservation.go
│   │   ├── management.go
│   │   └── metrics.go
│   ├── playerauth/auth.go        # 현재 플레이어 토큰
│   ├── operatortoken/token.go    # 운영자 토큰 파일 검사
│   ├── protocol/
│   │   ├── codec.go              # relay.v2 메시지 검사
│   │   └── id.go                 # 식별자 검사
│   └── httpx/
│       ├── httpx.go              # HTTP 입력·오류·입장 제한
│       └── ip.go                 # 신뢰 프록시와 접속자 IP
├── api/relay/v2/
│   ├── relay.proto
│   └── relay-baseline.binpb       # v2 계약의 breaking 검사 기준
├── gen/go/relay/v2/relay.pb.go
├── client/
│   ├── src/relay/                # 공용 TS SDK와 생성 메시지
│   ├── src/demo/                 # 게임 검증 화면
│   ├── scripts/                  # SDK·호환·복구 검사
│   ├── android/                  # Capacitor Android
│   └── MinbWebKit/               # 고정한 외부 서브모듈
├── infrastructure/
│   ├── config/                  # 역할별 env·nodes·트래픽 예산
│   ├── ops/                     # Caddy·systemd·비용 가드
│   ├── scripts/                 # 패키징·배포·토큰·비용 가드
│   │   └── tests/               # 임시 호스트로 검사
│   ├── docs/operations.md
│   └── README.md
├── scripts/                     # 도구 설치·프로토콜·부하 검사
├── docs/                        # 현재 v2 요구사항·설계·증거
├── .planning/                   # 현재 v2 관리 상태
├── .github/workflows/ci.yml
├── Makefile
├── buf.yaml
├── buf.gen.yaml
├── go.mod
├── go.sum
└── README.md
```

Go 테스트는 구현 파일 옆에 둔다. 위 구조는 책임을 설명하려고 `_test.go`를 생략했다.
운영 모듈 이름에는 v2 접미사를 붙이지 않는다. wire 계약과 생성물에는 `relay/v2` 경로를 유지한다.
단일 서버이므로 `app/wss`, `app/lobby` 분기는 만들지 않는다.
배포 대상도 하나이므로 `infrastructure/v2`를 별도 보존하지 않는다.

## 목표와 범위

이 저장소를 v2 전용 HTTPS·WSS 서버로 정리한다.
기능별 모듈 구조와 기존 `Allocator`를 유지하면서 매칭·예약·연결 오류를 수정한다.
사용자가 v1 제거를 명시했으므로 이전 계획의 v1 호환·회귀 보존 조건을 폐기한다.
이전 구현은 Git 이력에서 확인한다. 실행 가능한 legacy 폴더나 호환용 위임 패키지를 만들지 않는다.

보존 대상은 현재 v2 HTTP·wire·SDK·CLI 계약이다.
`standalone`, `coordinator`, `relay`는 같은 바이너리의 실행 역할로 유지한다.
Go 버전은 `go.mod`의 `1.26.10`을 유지하고 새 런타임 의존성을 추가하지 않는다.
게임 판정, Redis, DB, Kubernetes, 노드 사이 방 이동과 배정기 다중 작성자는 범위 밖이다.
실제 호스트 변경·배포·push·merge는 향후 로컬 구현 범위에도 포함하지 않는다.

## 제거·이동·추출 범위

| 현재 대상 | 계획한 처리 | 이유 |
|---|---|---|
| `cmd/lobby-relay`, 기존 `internal/app` 구현과 검사 | 삭제 | v1 서버 실행 경로 |
| `internal/clock`, `matchmaking`, `relayroom`, `relaytransport`, `udprelay`, `wsrelay` | 구현과 전용 검사 삭제 | v1 매칭·UDP·carrier 경로 |
| `internal/httpapi/operatorapi`, `playerapi` | 필요한 토큰 파싱을 추출한 뒤 삭제 | v1 HTTP 경로 |
| `internal/httpapi/httpx` | `internal/httpx`로 이동 | v2가 사용하는 HTTP 기능 |
| 기존 `internal/protocol` | ID 검사만 추출하고 v1 codec·HMAC·fixture·전용 검사 삭제 | v1 relay 인증과 wire 제거 |
| `internal/protocolv2` | `internal/protocol`로 이동 | 남는 relay 계약은 v2 하나 |
| `internal/controlv2`, `coordv2`, `relayv2` | `control`, `coordinator`, `relay`로 이동 | 기능 기준 이름 통일 |
| `api/relay/v1`, `gen/go/relay/v1` | 삭제 | v1 계약과 생성물 |
| `test/compat/csharp`, `unity`, `global.json` | 삭제 | v1 C#·Unity 검증과 .NET 설정 |
| 기존 v1 systemd·Caddy·UDP 검사·호스트 설치 | 삭제 | 실행할 v1 배포 대상이 없음 |
| 기존 토큰 설치·배포 보호·비용 가드 | 필요한 보호 동작을 v2 대상으로 전환 | v2에서도 필요한 운영 기능 |
| `infrastructure/v2` | 현재 설정을 flat infrastructure 구조로 이동한 뒤 삭제 | 두 배포 경로 제거 |
| v1 전용 계획·ADR·증거·handoff | 현재 문서에서 제거하고 링크 정리 | Git 이력으로 보관 |

`internal/clock`은 현재 v1 패키지만 사용한다. v2 때문에 보존하지 않는다.
`ValidID`, `MaxIDBytes=64`, 운영자 토큰 파싱과 파일 검사는 소비자를 확인한 뒤 옮긴다.
v1 폴더를 삭제하기 전에 현재 v2 호출자가 새 위치를 사용하도록 바꾼다.

`playerauth`의 `tokenVersion=1`과 `player-token/v1` HMAC 문자열은 현재 v2 인증에도 사용된다.
이 값은 relay.v1 패킷 계약과 다르며, 이름 정리를 이유로 바꾸지 않는다.
`snake-demo.v1`, `demo.v1`, `load.v1`도 게임 계약의 이름이므로 유지한다.
v1 서버에서만 사용하는 인증 설정 분기는 현재 v2 토큰 결과를 보존하는 범위에서 제거한다.

## 의존성과 상태 소유권

`cmd/wss-relay`는 신호와 종료 코드를 처리한다. `app`이 설정·의존성·listener·종료를 조립한다.
`app`은 기능 모듈을 사용한다. 기능 모듈은 `app`을 import하지 않는다.
`coordinator`는 `control`의 `Allocator`를 구현한다. `control`은 `coordinator`를 import하지 않는다.
`protocol`, `httpx`, `operatortoken`은 실행 구성을 알지 못한다.

`control.Manager`가 로비·티켓·참가자 소속·만료·배정 상태와 잠금을 소유한다.
HTTP `Server`는 인증·CORS·입장 제한·디코딩·라우트·응답 변환을 소유한다.
Manager에는 요청·응답 객체를 전달하지 않고 검증한 참가자 ID를 전달한다.
잠금 안에서 참가자 슬라이스와 ready 맵을 복사하고, 잠금 밖에서 JSON을 인코딩한다.
업무 오류는 작은 구체 타입으로 표현한다. Manager 인터페이스와 메모리 저장소 인터페이스는 추가하지 않는다.

`relay`의 `server.mu → room.mu → client.mu` 순서와 연결별 writer를 유지한다.
상태 병합·사건 큐·접속권·세대·방 단일 소유와 노드 incarnation 검사를 유지한다.
방 worker, actor, 이벤트 버스와 잠금 분할은 추가하지 않는다.

## 배정과 만료

현재 `Allocator`의 `Allocate`와 `Refresh` 역할을 유지한다.
성공은 해당 방과 참가자 구성의 확정 예약이다.
`ErrCapacity`, `ErrInvalid`, `ErrClosed`는 미확정 예약을 남기지 않는 확정 거부일 때만 사용한다.
`ErrGone`은 해당 권한의 종료를 뜻한다. 그 밖의 오류는 참가자 구성을 고정하고 같은 ID로 재시도한다.
기존 소유 노드의 unhealthy와 프록시의 빈 503은 확정 거부가 아니다.
노드의 제한된 JSON 응답에서 `allocation_failed`를 확인한 503만 확정 거부로 해석한다.

수동 로비와 FIFO가 배정 준비·실행·확정 규칙을 공유한다.
준비와 확정은 잠금 안에서, 원격 호출은 잠금 밖에서 수행한다.
늦은 성공은 종료된 그룹을 되살리지 못한다.
개별 대기 티켓은 2분이며, 그룹 형성 후 모든 티켓은 형성 시각부터 2분을 공유한다.
배정 중에는 그룹과 티켓을 함께 보존한다. 성공하면 배정 만료를 함께 적용한다.
실패 후 재시도는 형성 기한을 연장하지 않으며, 만료 시 소속과 티켓을 함께 해제한다.

`QueueP95UpperMs=-1`은 5초 히스토그램 상한 초과다.
응답 형식을 유지하면서 배정기는 음수와 100ms 초과를 모두 신규 배정에서 제외한다.

## 인증과 연결 수명주기

운영자 토큰 파일은 일반 파일·0600 권한·최대 46바이트 읽기를 요구한다.
43바이트 canonical raw base64url에 LF 또는 CRLF 한 개를 붙일 수 있다.
symlink·파일 교체·추가 공백·과한 크기·전부 0인 키를 거부한다.
기존 v1 구현에서 이 검사를 추출하되, v1 실행 경로를 유지하지 않는다.

v2 `app`의 모든 listener와 서버·중계 연결은 생성한 주체가 정리한다.
v1 listener 누수·keepalive·업그레이드 종료를 수정하는 작업은 폐기한다. 해당 서버를 삭제하기 때문이다.

SDK는 연결 epoch로 이전 인증 갱신의 성공·실패·finally를 격리한다.
새 연결·close·leave·suspend 뒤에는 이전 작업이 현재 연결이나 타이머를 바꾸지 못한다.
HTTP 요청의 기존 5초 제한은 응답 헤더와 본문 읽기 전체에 적용한다.
검증 화면은 재시도 가능한 조회 실패 후 750ms 뒤 다시 조회하고, 종료·폐기 후 이전 조회를 무효화한다.

## 프로토콜과 CI

현재 v2 스키마의 필드 번호·package·서브프로토콜과 Go·TS 생성물 의미를 보존한다.
v1 스키마·fixture·생성 단계와 C#·.NET 설치·검사 단계를 제거한다.
v2 전용 baseline은 변경 전 v2 스키마에서 만든다. 수정 후 스키마로 기준을 자동 갱신하지 않는다.
기존 aggregate baseline과 `api/relay/v1/relay-v1.binpb`는 새 기준을 검증한 뒤 제거한다.
Go v2와 TS v2를 staging에서 모두 생성하고, lint·breaking·생성물 차이를 하나의 gate로 검사한다.
`cmd/wire-check`의 17종 Go→TS→Go 왕복과 uint64 최댓값 검사를 유지한다.
도구는 기존 고정 버전·체크섬을 유지하며 Darwin/arm64와 Linux/x86_64를 지원한다.

## 단일 배포 경로

`infrastructure/config`, `ops`, `scripts`, `docs`를 현재 WSS 배포의 유일한 경로로 사용한다.
새 스크립트는 v1 호환 CLI·경로·계정을 제공하지 않는다.
패키징은 `package-release.sh OUTPUT.tar.gz`, 배포는 `deploy-backend.sh ARCHIVE COMMIT_SHA INSTANCE`다.
INSTANCE는 standalone·coordinator·relay이며, 초기 배포 경로는 호스트당 인스턴스 하나를 지원한다.
경로는 `/opt/wss-relay/releases/SHA/wss-relay`, current, `/etc/wss-relay/INSTANCE.env`를 사용한다.
archive 경로 검사·크기 제한·체크섬·배포 잠금·원자적 승격·실패 롤백을 기존 구현에서 옮긴다.
live 토큰·CA·nodes·traffic 설정을 예제로 덮어쓰지 않는다.
전환 전에는 env·참조 파일·권한·Caddy·systemd를 검사한다. env를 셸 코드로 실행하지 않는다.
바이너리 dry-run은 가정하지 않는다. 실제 시작은 전환 후 healthz·readyz로 확인한다.
롤백은 이전 실행 파일과 서비스 구성을 복원한다. 메모리의 진행 매치는 복구하지 못한다.

토큰 설치 도구는 `/etc/wss-relay/secrets`, `wss-relay` 계정을 기본으로 사용한다.
`--directory`, `--service-user`, `--filename`으로 실제 역할별 토큰을 설치한다.
파일 이름은 단일 basename만 허용하고, 기존 토큰은 교체하지 않는다.
sshd·UFW를 자동 변경하는 v1 `setup-host.sh`는 제거한다. 새 호스트 자동 설정 도구는 추가하지 않는다.
계정·디렉터리·방화벽의 수동 준비 조건은 운영 문서에 적는다.

초당 Protobuf 예산과 월별 NIC 비용 가드를 구분한다.
기존 durable 장부·stop marker·손상 시 차단 동작을 v2 서비스와 상태 경로로 옮긴다.
월 한도 초과 시 선택한 `wss-relay@INSTANCE.service`와 Caddy를 차단한다. UDP 검사나 v1 서비스를 사용하지 않는다.
비용 가드가 누락·손상되면 서비스 시작과 배포를 거부한다. 이전 장부를 새 장부로 조용히 초기화하지 않는다.
신규 호스트의 최초 장부는 관리자가 명시적으로 초기화한다. 기존 호스트의 장부 전환은 별도 회계 확인 후 수행한다.

## 구현 순서와 완료 기준

1. v2가 사용하는 ID·토큰·HTTP 기능을 추출한다.
2. v1 코드·계약·생성물·검사와 불필요한 도구 참조를 제거한다.
3. v2 패키지를 기능별 이름으로 옮기고 매칭·예약 오류를 수정한다.
4. Manager와 실행 구성을 분리한 뒤 SDK·CI·운영 도구를 정리한다.
5. 현재 문서와 v2 관리 상태를 갱신하고 전체 검사를 실행한다.

완료 시 v1 서버·UDP·Unity·C# 패키지가 현재 소스·빌드·CI·배포 경로에 없어야 한다.
문자열 `v1`의 일괄 부재를 검사 기준으로 삼지 않는다. 활성 인증과 게임 계약은 위에서 구분했다.
v2 HTTP·wire·SDK·CLI·분산 복구와 경합·정적·포맷 검사가 통과해야 한다.
protocol gate는 임시 schema-only 변경을 탐지해야 한다.
임시 호스트 검사는 v2 승격·롤백·설정 보존·비용 차단을 확인해야 한다.
실제 Linux 배포·원격 CI·Android 실기기·무선 망·전체 부하는 실행한 범위만 보고한다.

## 근거

- [공식 Go 구성](https://go.dev/doc/modules/layout#server-project): cmd와 internal 중심의 서버 구조.
- [Go 인터페이스 지침](https://go.dev/wiki/CodeReviewComments#interfaces): 실제 호출자가 필요한 인터페이스 소유.
- [현재 기술 계약](../../TRD.md), [현재 공개 API](../../specs/2026-10-22-control-v2-http.md).

기존 조사에서 Go 경합·정적·포맷과 Buf 검사를 실행했고, 매칭 기한·이전 SDK 요청 오류를 재현했다.
이 결과는 향후 제거와 리팩터링 후 검사를 대신하지 않는다.
