# go-wss-relay

웹·Android 공용 WSS 전용 중계 서버를 개발하는 저장소다.
[원본](https://github.com/game-projects97/go-lobby-relay)을 포크했으며 기존 이력과 라이선스를 보존한다.

게임 좌표와 모션은 클라이언트가 계산한다. 서버는 참가자를 검증하고 상태와 이벤트를 중계한다.
서버는 이동·충돌·승패를 판정하지 않는다. 경쟁 게임의 판정 권한은 별도로 설계해야 한다.

## 현재 구현

- `/v2/relay`: `relay.v2` 서브프로토콜, 바이너리 Protobuf 메시지.
- 단회 접속권, 방별 참가자 격리, 재접속 토큰 교체와 세대 증가.
- 참가자별 최신 상태 보관, 50 ms 간격 상태 묶음 전송.
- 이벤트 순서 보존과 접수 확인. 재접속 이후 이벤트 재전송은 제공하지 않는다.
- 메시지 크기와 빈도 제한. 느린 연결의 대기열은 32개·256 KiB로 제한한다.
- 기본 상한: 방 256개, 동시 연결 1,024개, 방당 참가자 16명. 성능 보장 수치는 아니다.
- 관리 API의 방 생성, 방 만료, 종료 처리.

`EventAccepted`는 서버 접수만 확인한다. 상대 클라이언트의 수신을 보장하지 않는다.
전체 상태 스냅샷이 약 64 KiB를 넘을 수 있어 신뢰성 대기열 상한을 256 KiB로 정했다.

## 로컬 실행

Go 버전은 `go.mod`를 따른다. macOS arm64에서는 `make tools`로 고정 도구를 설치할 수 있다.

```sh
make tools
python3 - <<'PYTOKEN'
import os
import secrets
fd = os.open('/private/tmp/wss-relay-token', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as target:
    target.write(secrets.token_urlsafe(32))
PYTOKEN
.tools/go/bin/go run ./cmd/wss-relay \
  --operator-token-file /private/tmp/wss-relay-token \
  --public-url ws://127.0.0.1:8090/v2/relay \
  --allowed-origin localhost:5173
```

기존 토큰 파일이 있으면 생성 명령은 실패한다. 기존 파일을 재사용하거나 다른 경로를 지정한다.
기본 공개 수신 주소는 `127.0.0.1:8090`, 관리 수신 주소는 `127.0.0.1:8091`이다.
운영 환경에서는 TLS 프록시 뒤에 배치하고 `--public-url wss://도메인/v2/relay`를 지정한다.
프록시의 WebSocket 업그레이드와 유휴 시간을 설정해야 한다.

신뢰하는 백엔드가 관리 API `POST /v2/rooms`를 호출한다.
`Authorization: Bearer <파일의 토큰>` 헤더와 다음 JSON 본문을 사용한다.

```json
{"roomId":"room-1","applicationProtocol":"snake.v1","participants":["alice","bob"],"ttlSeconds":1800}
```

응답에는 `websocketUrl`, `roomInstanceId`, 참가자별 `tickets`가 포함된다.
백엔드는 로그인한 사용자에게 해당 사용자의 접속권만 반환해야 한다.
관리 API는 루프백 IP에서만 수신한다. 브라우저에 운영자 토큰이나 전체 접속권을 전달하면 안 된다.

## 검증

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go test -race ./...
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go vet ./...
make protocol-check
make relay-build
```

테스트는 실제 WS·TLS WSS 연결, 상태·이벤트 전달, 방 격리, 접속권 재사용 거부,
Origin 거부, 재접속, 스냅샷 순서, 대기열 상한, 관리 API 인증을 확인한다.
Go 코드 생성은 v1·v2를 포함한다. 기존 C# 산출물과 `csharp-compat`는 원본 이력으로 남겨 둔다.

## 남은 작업

현재 구현은 단일 노드 기반이다. 운영 배포가 완료된 상태는 아니다.
TypeScript SDK, 사용자 인증·매칭 연동, HTTP 접속권 재발급, 노드 분산과 드레이닝,
노드별 전송량 예산, IP별 접속 제한, Android 실기기 시험, 부하 시험이 남아 있다.
상세 명세의 WebSocket 종료 코드 매핑도 추가해야 한다.
기존 배포 스크립트와 Unity 문서는 원본용이며 v2 운영 배포에 그대로 사용할 수 없다.

- [전체 계획](docs/plans/2026-10-20-web-android-wss-relay-refactor.md)
- [메시지 명세](docs/specs/2026-10-20-relay-v2-message-spec.md)
- [실제 스키마](api/relay/v2/relay.proto)

계획의 레퍼런스 설명은 설계 배경이다. 이 저장소의 구현을 Snake.io의 실제 통신 규격으로 해석하면 안 된다.
