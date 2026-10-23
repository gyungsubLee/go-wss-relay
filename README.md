# go-wss-relay

웹 TypeScript 게임과 Android WebView 앱을 위한 WSS 중계 서버와 공용 TS SDK다.
[원본](https://github.com/game-projects97/go-lobby-relay)을 포크했으며 이력과 라이선스를 보존한다.

서버는 인증·로비·FIFO 매칭·방 배정·상태와 이벤트 전달을 담당한다.
게임 클라이언트가 모션·보간·충돌·승패를 계산한다. 서버 접수 확인은 상대의 적용 완료를 보장하지 않는다.

## 구현

- 바이너리 Protobuf·relay.v2, 일회 접속권, 참가자별 권한, 세대별 재접속·캐시.
- 50 ms 상태 묶음과 최신 상태 병합. 사건 큐는 32개·256 KiB로 제한한다.
- 플레이어 토큰 갱신, 공개·비공개 로비, ready/revision, FIFO, 개인 접속권 재발급.
- MinbWebKit 4.0 기반 검증 화면과 게임에 독립적인 TS SDK. Capacitor Android APK·AAB 구성.
- standalone/coordinator/relay 역할, 노드 예약·등록·단일 소유·드레이닝·활성 방 복원.
- IP·참가자·방·노드 제한, 바이트 예산, 큐·쓰기·자원 계측과 실제 TLS 부하 도구.

2026-10-23 기준으로 v2 전용 구조와 로컬 검사를 마쳤다. Task 10 독립 리뷰와 전체 브랜치 리뷰를 마쳤다. 최종 I1 수정도 재리뷰에서 닫았다.
Android 실기기·무선 망·실제 Linux 배포·원격 CI는 아직 검증하지 않았다.
현재 증거와 남은 요구사항은 [완료 감사](docs/evidence/v2/completion-audit.md)를 확인한다.

## 로컬 실행

```sh
git submodule update --init
make tools
python3 - <<'PYTOKEN'
import os, secrets
os.makedirs('.cache', exist_ok=True)
fd = os.open('.cache/wss-demo.token', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as target:
    target.write(secrets.token_urlsafe(32))
PYTOKEN
.tools/go/bin/go run ./cmd/wss-relay \
  --operator-token-file .cache/wss-demo.token \
  --public-url ws://127.0.0.1:8090/v2/relay \
  --allowed-origin 127.0.0.1:5173 \
  --allowed-origin localhost:5173
```

기존 토큰 파일이 있으면 생성 명령은 실패한다. 기존 파일을 재사용한다.
다른 터미널에서 `cd client`, `npm ci`, `npm run dev`를 실행한다.
Chrome 두 창에서 http://127.0.0.1:5173 에 접속한다.
한쪽에서 방을 만들고 다른 쪽에서 코드로 참가한다. 둘 다 준비한 뒤 소유자가 시작한다.
화면 클릭으로 이동하고 이벤트를 보내면 상대 화면의 좌표와 기록이 바뀐다.
빠른 매칭은 두 창에서 상대 찾기를 누른다. 검증 화면은 한 페이지에서 한 매치를 다루며, 종료 후 새로고침으로 다시 시작한다.

기본 수신은 127.0.0.1:8090, 관리 수신은 127.0.0.1:8091이다.
CLI 기본 상한은 512개 방·200개 참가자 슬롯, 방당 최대 정원은 16명이다.
이 값은 운영 성능 보장이 아니다. 대상 호스트에서 지연·전송량을 측정해 확정한다.
운영 HTTPS·WSS·노드 분산은 [배포 절차](infrastructure/README.md)와 [운영 절차](infrastructure/docs/operations.md)를 따른다.
v1 서버·UDP·C#·Unity 실행 경로는 제거했다. 이전 구현은 Git 이력에서 확인한다.

## SDK와 Android

[RelayClient](client/src/relay/client.ts)는 연결·바이너리 인코딩·상태 병합·이벤트·복귀를 담당한다.
[LobbyClient](client/src/relay/api.ts)는 인증·로비·매칭·개인 접속권 API를 담당한다.
렌더링·전체 상태 생성·수신 상태 적용은 게임 코드가 담당한다.
검증 화면의 [뱀 전체 상태·권한자 계약](docs/specs/2026-10-22-snake-demo.md)은 SDK와 분리한다.

```sh
cd client
VITE_RELAY_API_URL=https://실제-배정기-도메인/v2/api npm run android:sync
```

동기화 후 설치된 JDK 21·Android SDK로 Gradle assembleDebug 또는 bundleDebug를 실행한다.
`prepare-android-dev.mjs`는 localhost 디버그 CA를 쓰는 로컬 시험에만 필요하다.
개발 인증서는 릴리스에 포함하지 않는다. 디버그 APK·AAB는 Play 출시 서명 산출물이 아니다.
USB 기기로 localhost 서버에 연결할 때는 해당 포트의 `adb reverse`가 필요하다.
프로세스 종료와 30초 유예 만료는 새 인증·재입장을 요구할 수 있다.

## 검증

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go test -race ./...
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go vet ./...
make protocol-check
make relay-build
cd client
npm run build
node --disable-warning=ExperimentalWarning --experimental-transform-types scripts/check-demo-entry.mjs
npm run check
```

[데모 진입 검사](client/scripts/check-demo-entry.mjs)는 실제 production 핸들러의 중복 진입·실패·퇴장·pagehide를 확인한다.
외부 API·타이머·DOM 경계는 대체한다. 실제 Chrome 검사는 [구조 변경 증거](docs/evidence/v2/2026-10-23-structure-refactor.md)의 최종 I1 절을 따른다.
npm run check는 위 로컬 서버가 필요하다. 분산·호환 검사는 아래 바이너리를 먼저 빌드한다.

```sh
make relay-build
.tools/go/bin/go build -o out/wire-check ./cmd/wire-check
.tools/go/bin/go run ./cmd/dev-tls
node --disable-warning=ExperimentalWarning --experimental-transform-types client/scripts/check-wire.ts
NODE_EXTRA_CA_CERTS="$PWD/.cache/dev-tls/cert.pem" node --disable-warning=ExperimentalWarning --experimental-transform-types client/scripts/check-cluster.ts
```

개발 인증서 생성은 새 디렉터리에서 한 번 실행한다. 기존 키·인증서를 덮어쓰지 않는다.
17종 메시지의 Go→TS→Go 왕복 검사, 실제 SDK 소켓 검사, 실제 노드 프로세스 검사를 구분한다.
서버 명령은 `cmd/wss-relay` 하나다. `internal/app`이 실행을 조립하고 `control.Manager`가 로비·FIFO 상태를 소유한다.
`coordinator`는 노드 배정을, `relay`는 방과 연결을 소유한다. [기술 계약](docs/TRD.md)에 책임 경계를 적었다.
현재 플레이어 토큰의 `tokenVersion=1`·`player-token/v1`과 게임 계약의 `.v1` 식별자는 유지한다.
2026-10-23 검사 명령·소스 해시·Chrome 화면·미실행 조건은 [구조 변경 증거](docs/evidence/v2/2026-10-23-structure-refactor.md)에 있다.

- [원래 1-5단계 계획](docs/plans/2026-10-20-web-android-wss-relay-refactor.md)
- [WSS 메시지 명세](docs/specs/2026-10-20-relay-v2-message-spec.md)
- [공개 HTTP API](docs/specs/2026-10-22-control-v2-http.md)
- [실제 스키마](api/relay/v2/relay.proto)

레퍼런스 설명은 설계 배경이다. 이 구현을 Snake.io의 실제 통신 규격으로 해석하지 않는다.
