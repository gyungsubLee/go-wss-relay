# v2 전용 Go 서버 리팩터링 실행 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans after the user explicitly authorizes implementation. Steps use checkbox syntax for tracking.

**Goal:** v1 레거시를 제거하고 v2 전용 기능별 구조·검사·배포 경로를 완성한다.

**Architecture:** app은 실행 구성을, control.Manager는 매칭 상태를, coordinator는 노드 소유권을, relay는 방과 WSS 연결을 소유한다. 기존 Allocator를 유지한다.

**Tech Stack:** Go 1.26.10, net/http, coder/websocket, Protobuf, TypeScript, Node 24, Python 3, Bash, 기존 Buf·protoc 도구.

**Spec:** [v2 전용 설계서와 폴더 구조](../specs/2026-10-23-go-structure-refactor-design.md)

## Global Constraints

- 사용자는 2026-10-23에 이 구조의 구현과 v2 회귀 검사를 승인했다.
- 현재 `codex/wss-v2` 브랜치에서 순차 구현하고 작업별 로컬 커밋을 만든다.
- v1 서버·UDP·C#·Unity 호환성을 제공하지 않는다. 이전 구현은 Git 이력에서 확인한다.
- `cmd/wss-relay`, 현재 v2 CLI 플래그·기본값·HTTP·wire·SDK 계약을 유지한다.
- `relay.v2`의 Protobuf package·필드 번호·서브프로토콜과 생성물 의미를 보존한다.
- Go 1.26.10과 기존 런타임 의존성만 사용한다.
- 방당 최대 16명, 기존 큐·메시지·바이트 제한, 접속 세대와 단일 노드 소유를 유지한다.
- 현재 playerauth의 활성 토큰 형식과 HMAC domain 문자열을 이름 정리 때문에 바꾸지 않는다.
- 실제 호스트 변경·배포·push·merge는 향후 로컬 구현 범위에도 포함하지 않는다.
- 한국어는 STE-KO 문서 표면을 적용한다. 생성물은 수동 수정하지 않는다.

## Review Focus

- v1 폴더 삭제로 v2 ID·토큰 검사가 사라지는 경우: 먼저 추출하고 기존 보안 동작을 검증한다. Task 1-2.
- 배정 응답 유실·unhealthy·빈 503: 참가자 구성을 유지하고 같은 노드에 재시도한다. Task 3.
- 종료 후 늦은 배정 성공 또는 이전 SDK finally: 끝난 소속과 새 연결을 바꾸지 못한다. Task 3·6.
- baseline 교체로 v2 breaking change가 숨는 경우: 변경 전 v2 계약을 별도 기준으로 고정한다. Task 2·8.
- 비용 장부 유실·손상 또는 배포 실패: 서비스를 차단하고 기존 장부·설정·실행 파일을 보존한다. Task 9.

## 실행 조건과 순서

Task 1-2는 의존성 구출과 제거, Task 3-5는 Go 구조·동작, Task 6-7은 SDK·화면, Task 8-10은 CI·운영·통합이다.
같은 파일을 수정하는 구현은 순차 실행한다. 독립된 읽기 전용 조사와 리뷰만 병렬로 진행할 수 있다.
Task 2의 경로 이동 이후 모든 작업은 새 경로를 사용한다.
삭제할 v1 서버의 오류를 고치거나 v1 회귀 검사를 확장하지 않는다.
현재 브랜치와 사용자 변경을 작업마다 확인한다. 사용자가 승인한 현재 작업 공간에서 진행한다.

## 검사 명령의 의미

아래 명령으로 구현을 검사한다.

```sh
GOTOOLCHAIN=local GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go test -race ./...
GOTOOLCHAIN=local GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go vet ./...
test -z "$(.tools/go/bin/gofmt -l cmd internal)"
```

집중 검사도 같은 Go 환경을 사용한다. 로컬 포트 거부와 제품 오류를 구분한다.
새 동작과 오류 수정은 실패한 회귀 검사를 먼저 확인한다. 순수 경로 이동에는 불필요한 테스트를 만들지 않는다.
Go 수정 전에 Modern Go Guidelines CLI의 해당 버전 지침 전체를 읽는다.

---

### Task 1: v2가 사용하는 기능을 레거시에서 추출한다

**Files:**
- Create: `internal/operatortoken/{token,token_test}.go`, `internal/protocol/id.go`, `id_test.go`
- Modify: `internal/protocol/codec.go`, `internal/httpapi/operatorapi/http.go`
- Modify: `cmd/wss-relay/main.go`, `internal/coordv2/pool.go`, 해당 테스트
- Source: `cmd/lobby-relay/main.go`, `main_test.go`의 보안 파일 읽기와 교체 검사

**Interfaces:**
- Produces: `operatortoken.Parse(encoded string) ([32]byte, error)`, `ReadFile(path string) ([32]byte, error)`.
- Produces: `protocol.ValidID(id string) bool`, `MaxIDBytes=64`를 wire 구현과 분리한다.
- Preserves: 현재 v2 플레이어 토큰의 값·서명·만료·재시작 후 검증 동작.

- [x] Step 1: 현재 ID·canonical 파싱 검사를 추출 위치로 옮기고 v2·노드 설정의 0644·symlink·파일 교체·과한 크기 거부를 검사한다.
- [x] Step 2: 부적절한 파일을 받아들이는 기존 v2 동작을 확인한다. 파일 시스템 거부나 컴파일 오류는 재현으로 계산하지 않는다.
- [x] Step 3: 이미 있는 보안 검사를 공용 위치로 옮겨 v2 호출자가 사용하게 한다. v1에 남는 참조는 다음 제거 작업을 위한 임시 상태로만 둔다.
- [x] Step 4: 공용 기능과 현재 v2 패키지 검사를 실행한다. 현재 StableKey 토큰이 같은 secret으로 새 issuer에서 검증되는지 확인한다.
- [x] Step 5: v1 전용 HMAC와 활성 playerauth HMAC를 구분했는지 검토한다. parser·reader에 HTTP 패키지 의존성이 없어야 한다.

### Task 2: v1을 제거하고 기능별 경로로 통합한다

**Files:**
- Delete: `cmd/lobby-relay`, 기존 `internal/app`의 v1 구현·전용 검사
- Delete: `internal/{clock,matchmaking,relayroom,relaytransport,udprelay,wsrelay}`
- Delete: `internal/httpapi/{operatorapi,playerapi}`와 v1 전용 protocol 구현·검사·fixture
- Delete: `api/relay/v1`, `gen/go/relay/v1`, `test/compat/csharp`, `unity`, `global.json`
- Move: `controlv2 → control`, `coordv2 → coordinator`, `relayv2 → relay`
- Move: `protocolv2 → protocol`, `httpapi/httpx → httpx`
- Create: `api/relay/v2/relay-baseline.binpb`
- Modify: import·Makefile·생성 설정·CI의 v1 참조

**Interfaces:**
- Consumes: Task 1의 ID·토큰 검사.
- Produces: v2만 포함한 Go package graph와 프로토콜 생성·검사 대상.
- Preserves: `api/relay/v2`, `gen/go/relay/v2`, TS v2 생성 경로와 현재 CLI.

- [x] Step 1: 변경 전 v2 스키마만으로 baseline을 만들고 descriptor의 package·필드 번호가 현재 v2와 같은지 확인한다.
- [x] Step 2: 실제 v2 import와 도구 참조를 목록화한다. auth 형식의 version 1과 게임 계약의 `.v1`을 삭제 대상으로 삼지 않는다.
- [x] Step 3: 위 v1 대상과 전용 검사를 제거하고 v2 import·경로를 이동한다. v1 수정 작업·호환 wrapper·C# 검사와 생성 루프도 제거한다.
- v2 codec을 protocol로 옮기면 기존 protocol self-import를 제거하고 같은 패키지의 ID 검사를 직접 사용한다.
- [x] Step 4: Go list·전체 경합·정적·포맷과 v2 Go·TS wire 검사를 실행한다. 생성물과 빌드·CI에 남은 v1 실행 참조를 확인한다.
- [x] Step 5: 기존 aggregate baseline은 검증한 v2 기준으로 대체한다. v1-only 의존성이 go.mod·go.sum에 남는지 확인하고 필요할 때만 tidy한다.

### Task 3: 배정 결과와 FIFO 기한을 통일한다

**Files:**
- Modify: `internal/control/{http,lobby,queue,allocation}.go`, `http_test.go`
- Modify: `internal/coordinator/pool.go`, `pool_test.go`. 이 단계에서는 아직 Task 5의 파일 분리를 적용하지 않는다.

**Interfaces:**
- Preserves: 기존 `Allocator.Allocate`와 `Refresh`의 역할과 메서드 수.
- Produces: 확정 거부·권한 종료·결과 불명의 오류 의미와 유효한 배정만 확정하는 처리.

- [x] Step 1: 아래 회귀 검사를 먼저 작성한다.
  - `TestFormedQueueRetainsNearExpiryTicket`: 119초 대기 후 배정 중 2초 경과. 두 참가자가 모두 개인 배정을 받는다.
  - `TestFormedQueueExpiryReleasesAllParticipants`: 그룹 형성 후 배정 실패와 기한 경과. 모두 다시 등록할 수 있다.
  - `TestLostReplyThenUnhealthyNodeKeepsRosterFrozen`: leave·ready 변경은 409이며 복구 후 같은 방을 받는다.
  - `TestProxy503KeepsReservationOwner`: 프록시의 빈 503 뒤에도 다른 노드에 방이 생기지 않는다.
  - `TestDefinitiveCapacityRefusalUnlocksLobby`: 노드의 `allocation_failed` 거부 후 수동 로비 수정이 가능하다.
  - `TestAllocationCompletionDoesNotReviveEndedMatch`: 늦은 성공이 종료 상태와 해제한 소속을 되돌리지 않는다.
- [x] Step 2: 두 패키지의 해당 테스트를 `-count=1`로 실행해 실제 실패를 확인한다.
- [x] Step 3: 형성 기한·미확정 예약·원격 응답 분류·확정 전 유효성 검사를 구현한다. 원격 호출은 잠금 밖에서 수행한다.
- [x] Step 4: 집중 검사와 두 패키지의 전체 `-race` 검사를 통과시킨다. 노드 재시작 fencing과 단일 소유 검사를 유지한다.
- [x] Step 5: 로비 시작과 FIFO가 같은 배정 규칙을 사용하는지 검토한다. 재시도가 그룹 기한을 무한 연장하지 않아야 한다.

### Task 4: 제어 상태를 Manager로 분리한다

**Files:**
- Create: `internal/control/{manager,views,errors}.go`, `manager_test.go`
- Modify: `internal/control/{http,lobby,queue,allocation}.go`, `http_test.go`

**Interfaces:**
- Produces: `NewManager(allocator Allocator, now func() time.Time) (*Manager, error)`.
- Produces: 아래 업무 메서드와 내부 상태를 공유하지 않는 응답 값.

```go
Create(player string, capacity int, application, visibility string) (Lobby, error)
Get(player, id string) (Lobby, error)
Preview(id string) (LobbyPreview, error)
List(application, cursor string, limit int) (LobbyPage, error)
Join(player, id string, revision uint32) (Lobby, error)
Ready(player, id string, revision uint32, ready bool) (Lobby, error)
Leave(player, id string, revision uint32) (*Lobby, error)
Start(ctx context.Context, player, id string, revision uint32) error
Assignment(ctx context.Context, player, id string, refresh bool) (Assignment, error)
Enqueue(ctx context.Context, player string, capacity int, application string) (MatchTicket, error)
Ticket(ctx context.Context, player, id string) (MatchTicket, error)
Cancel(player, id string) error
Expire()
Restore(allocation Allocation, participants []string) error
EndMatch(id, instance string)
ReconcileLocal(exists func(string) bool)
```

Leave의 nil은 204이며, 응답 타입은 현재 JSON 필드와 타입을 보존한다.
업무 오류 종류는 invalid·forbidden·not_found·conflict·gone·unavailable이다. HTTP에서 현재 상태 코드로 변환한다.

- [x] Step 1: `TestBlockedHTTPResponseDoesNotBlockManager`로 응답 쓰기 중 다른 로비 변경이 완료되는지 검사한다.
- [x] Step 2: 기존 전역 잠금 때문에 변경이 멈추는 실패를 확인한다. sleep 대신 채널과 실패 상한을 사용한다.
- [x] Step 3: 상태·잠금·업무 연산을 Manager로 옮긴다. HTTP는 입력 검증 → Manager 호출 → 응답 순서로 정리한다.
- [x] Step 4: `TestReturnedLobbyCannotMutateManager`와 기존 HTTP·Task 3 회귀 검사를 `-race`로 통과시킨다.
- [x] Step 5: 일반 경로와 오류 변환을 읽고 Manager 인터페이스·범용 저장소·전달용 계층이 추가되지 않았는지 확인한다.

### Task 5: 실행 구성과 노드 입장 판단을 정리한다

**Files:**
- Create: `internal/app/{config,run}.go`, 해당 테스트
- Modify: `cmd/wss-relay/main.go`, `main_test.go`
- Split: `internal/coordinator`를 config·node_client·placement·recovery·management로 분리
- Modify: `internal/coordinator`의 배정 판단, `internal/relay`의 지표 검사
- Split: `internal/relay/server.go`, `rooms.go`에서 connection·credentials 책임 분리
- 완료한 후속 작업: `internal/playerauth/auth.go`의 v1 전용 임의 키 옵션 제거. 활성 v2 토큰 바이트·HMAC·재시작 검증 유지.

**Interfaces:**
- Produces: `app.Run(ctx context.Context, args []string) error`. 서버 명령은 신호·종료 코드만 담당한다.
- Preserves: 현재 role·플래그·기본값·준비 상태·드레이닝과 Stats JSON의 `-1` 표기.
- Produces: 음수 또는 100ms 초과 p95인 노드의 신규 배정 거부.

- [x] Step 1: 세 역할의 시작·취소·listener 실패 정리·drain 종료와 `TestPlacementRejectsQueuePercentileOverflow`를 검사한다.
- [x] Step 2: 지표 상한의 기존 잘못된 배정을 재현한다. 순수 실행 코드 이동은 기존 동작 검사를 기준으로 삼는다.
- [x] Step 3: v2 실행 구성을 평평한 app에 옮기고 의미 있는 파일 분리를 적용한다. 잠금·writer·재접속 계약을 보존한다.
- [x] Step 4: app·cmd·coordinator·relay의 경합 검사와 실제 노드 프로세스 검사를 실행한다.
- [x] Step 5: 자원 생성·실행·정리가 한 흐름에서 보이고, app을 역으로 import하는 기능 패키지가 없는지 확인한다.

### Task 6: SDK의 이전 요청과 본문 제한을 격리한다

**Files:**
- Modify: `client/src/relay/{client,api}.ts`, `client/package.json`
- Create: `client/scripts/check-lifecycle.ts`

**Interfaces:**
- Preserves: SDK 공개 메서드·기존 요청 제한 5초·offline 사건 정책.
- Produces: epoch가 다른 성공·실패·finally가 현재 연결과 refresh 상태를 변경하지 않는 동작.

- [x] Step 1: 이전 갱신의 성공·실패·finally, 새 connect, suspend/resume, close 뒤 결과를 deferred promise로 검사한다.
- [x] Step 2: Node 내장 HTTP 서버로 헤더 후 본문을 멈춘다. 실제 LobbyClient가 5초 제한으로 중단되지 않는 오류를 확인한다.
- [x] Step 3: 기존 sessionEpoch 방식을 재사용하고 타이머 정리를 본문 읽기 뒤 finally로 옮긴다.
- [x] Step 4: `npm run check:lifecycle --prefix client`, TS 빌드와 실제 SDK 소켓 검사를 통과시킨다.
- [x] Step 5: 시험용 공개 옵션·메서드·새 프레임워크가 추가되지 않았는지 검토한다.

### Task 7: 검증 화면의 조회 복구를 유지한다

**Files:**
- Modify: `client/src/demo/main.ts`
- Test: `client/scripts/check-lifecycle.ts` 또는 실제 Chrome 증거

**Interfaces:**
- Consumes: `ControlError.retryable`, 현재 조회 간격 750ms, disposed·joined 상태.
- Produces: 일시 오류 뒤 조회 복구와 종료·퇴장 뒤 이전 조회 무효화.

- [x] Step 1: 로비·티켓 조회에서 429·503을 한 번씩 반환하고 다음 조회가 중단되는 현상을 확인한다.
- [x] Step 2: 403·404·410과 조회 중 퇴장에서 대기를 끝내는 조건을 확인한다.
- [x] Step 3: 기존 조회 코드에 명시적 오류·종료 분기를 적용한다. 범용 polling 프레임워크는 추가하지 않는다.
- [x] Step 4: 연결된 Chrome의 정확한 탭에서 두 참가자의 복구·종료를 확인한다. Playwright는 사용하지 않는다.
- [x] Step 5: TS 빌드와 SDK 검사를 실행한다. 브라우저 미검증 조건은 보고서에 남긴다.

### Task 8: v2 프로토콜 gate와 Linux CI를 연결한다

**Files:**
- Modify: `scripts/bootstrap-tools.sh`, `Makefile`, `buf.gen.yaml`, `buf.yaml`, `client/package.json`, `.github/workflows/ci.yml`
- Baseline: Task 2에서 고정한 `api/relay/v2/relay-baseline.binpb`

**Interfaces:**
- Produces: `make protocol-check`의 v2 lint·breaking·Go·TS 생성 차이 검사.
- Preserves: 고정 도구 버전과 checksum 검증. Linux/x86_64와 Darwin/arm64를 지원한다.
- Removes: v1 생성 루프·fixture·C#·.NET gate와 삭제한 배포 파일의 CI 호출.

- [x] Step 1: 임시 작업 사본의 schema-only 변경이 생성물 누락으로 탐지되는지 먼저 검사한다.
- [x] Step 2: 공식 배포 소스에서 플랫폼별 체크섬을 확인한다. baseline은 수정 후 스키마로 덮어쓰지 않는다.
- [x] Step 3: Go v2와 TS v2를 staging에서 모두 생성한 뒤 산출물을 갱신하고 CI에 protocol·SDK lifecycle 검사를 연결한다.
- [x] Step 4: 깨끗한 생성은 통과하고 임시 차이는 실패하며 복원 후 통과하는지 확인한다. 17종 wire 왕복과 uint64 최댓값을 유지한다.
- [x] Step 5: 로컬 Darwin 결과와 Linux CI 결과를 구분한다. 존재하지 않는 원격 CI 성공을 보고하지 않는다.

### Task 9: 운영 도구를 WSS 전용 한 경로로 전환한다

**Files:**
- Move: `infrastructure/v2/*` → `infrastructure/{config,ops,docs}`
- Replace: 기존 v1 package-release·deploy-backend·check-deployment와 전용 검사
- Adapt: 토큰 설치·traffic_budget·check-traffic-budget·timer·시작 전 보호와 해당 검사
- Delete: v1 Caddy·lobby-relay.service·UDP 검사·setup-host.sh와 전용 검사
- Modify: `infrastructure/README.md`, `infrastructure/docs/operations.md`, Makefile·CI 운영 검사 경로

**Interfaces:**
- Produces: 패키징 `OUTPUT.tar.gz`, 배포 `ARCHIVE COMMIT_SHA INSTANCE`.
- Produces: `/opt/wss-relay/releases/SHA/wss-relay`, current, `/etc/wss-relay/INSTANCE.env`.
- Produces: 토큰 설치의 기본 `/etc/wss-relay/secrets`, `wss-relay` 계정과 안전한 `--filename`.
- Preserves: archive 보호·체크섬·배포 잠금·원자적 승격·rollback, live 설정과 비밀정보 보존.
- Preserves: durable 비용 장부·영구 stop marker·손상 시 차단. v2 WSS 서비스와 Caddy를 대상으로 변경한다.

- [x] Step 1: 임시 root와 서비스 명령 대체로 세 역할의 승격·rollback·첫 설치 실패·archive 거부·설정 보존 검사를 작성한다.
- [x] Step 2: 토큰 이름·권한·교체와 비용 장부 유실·손상·재부팅·월 변경·한도 초과 검사를 v2 대상으로 고친다. 명시적 첫 초기화와 기존 장부 재초기화 거부도 검사한다.
- [x] Step 3: flat 구조에 v2 운영 도구를 적용한다. 초당 Protobuf 예산과 월별 NIC 가드의 파일·단위를 구분한다. env를 source하지 않는다.
- [x] Step 4: 배포·토큰·비용 가드 Python 검사와 쉘 문법·Linux 패키징을 실행한다. 새 설정 실패 전후 current·장부·live 파일을 확인한다.
- [x] Step 5: 호스트당 인스턴스 하나, 수동 준비 조건과 메모리 매치 비복구를 문서화한다. sshd·방화벽 자동 설정 도구는 추가하지 않는다.

### Task 10: v2 전체 검증과 현재 문서를 맞춘다

**Files:**
- Modify: `README.md`, `docs/{PRD,TRD}.md`, 현재 v2 명세, `.planning/{PROJECT,REQUIREMENTS,STATE,ROADMAP}.md`
- Remove or replace: v1 전용 ADR·계획·handoff·evidence·research와 현재 문서의 참조
- Create: `docs/evidence/v2/2026-10-23-structure-refactor.md`

**Interfaces:**
- Consumes: Task 1-9의 변경·검사·남은 조건.
- Produces: v2만 설명하는 현재 문서와 독립된 전체 리뷰.

- [x] Step 1: 전체 Go 경합·vet·포맷, v2 protocol gate, TS 빌드, SDK lifecycle·wire·실제 노드 프로세스 검사를 실행한다.
- [x] Step 2: Go package graph·빌드·CI·배포에 v1 실행 참조가 없는지 확인한다. 활성 playerauth와 게임 계약의 v1 식별자는 허용한다.
- [x] Step 3: WSS 배포·토큰·비용 가드 검사와 실제 Chrome의 로비·빠른 매칭·재접속·일시 오류 복구를 확인한다.
- [x] Step 4: 현재 문서를 새 구조에 맞추고 v1 전용 실행 문서를 제거한다. Git 이력을 수정하거나 별도 legacy 트리를 만들지 않는다.
- [ ] Step 5: diff와 실행 증거를 독립 리뷰에 전달한다. 중요한 발견사항을 해결하고 실제 Linux·Android·망·부하 미검증 조건을 보고한다.

## 자체 검토와 현재 상태

- v1 보존·수정·C# 호환 작업을 제거하고, 필요한 보호 동작의 v2 추출·전환으로 대체했다.
- 삭제 전에 공용 기능을 추출하고, baseline과 도구 참조를 함께 바꾸도록 순서를 정했다.
- 파일 이동 후 작업은 새 경로를 사용한다. 완료 기준은 v2 계약과 실제 동작 검사에 연결했다.
- Task 1-9는 구현·검사·개별 리뷰를 마쳤다. Task 5의 인증 옵션 제거 후속 작업도 닫았다.
- Task 10 Step 1-4는 소스가 같은 실행 증거와 현재 문서 검사로 확인했다. [구조 변경 증거](../../evidence/v2/2026-10-23-structure-refactor.md)를 따른다.
- Task 10 Step 5와 전체 브랜치 리뷰는 대기 중이다. 실제 Linux·Android·무선 망·전체 부하·원격 CI 미실행 조건은 유지한다.
