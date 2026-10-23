# relay.v2 메시지 명세

작성일: 2026-10-20

상태: 2026-10-23 현재 계약. 아래 스키마를 api/relay/v2/relay.proto에 적용했다.
서버·공용 TS SDK·HTTPS 접속권 재발급·분산 배정에 이 계약을 적용했다. 검증 범위와 남은 항목은 [진행 감사](../evidence/v2/completion-audit.md)를 따른다.
범위: 웹·Android 공용 WSS 중계. 좌표·충돌·점수의 게임 규칙은 애플리케이션 계약에서 정의한다.

## 1. 이전 계약과 비교

다음 표는 전환 배경이다. v1 실행 코드·스키마·생성물·전용 검사는 제거했다. 이전 구현은 Git 이력에 있다.

| v1 | v2 |
|---|---|
| UDP 데이터그램 또는 이를 운반하는 WSS | WSS 바이너리 메시지 |
| `Hello → Challenge → Auth → Bound` | `Connect → Welcome` |
| grant secret, 메시지별 HMAC, 주소 바인딩 | TLS, 단기 접속권, 연결에 묶인 참가자 권한 |
| 매 메시지의 room/session ID | 인증 시 확정한 방·참가자를 서버가 사용 |
| `ClientData → ServerData` 한 종류 | 최신 상태, 사건, 참가자 변경, 접속 제어 구분 |
| UDP·본문 상한 1,200·900바이트 | WSS 메시지와 본문 종류별 상한 |
| Go·C# 생성 | Go·TypeScript 생성 |

## 2. 전송과 버전

- WSS 경로는 `/v2/relay`, 서브프로토콜은 `relay.v2`다.
- WebSocket 바이너리 메시지 하나에 Envelope 하나를 담는다. 여러 Envelope를 단순 연결하지 않는다.
- WebSocket 프레임 분할은 라이브러리가 처리한다. 크기 제한은 조립한 메시지 전체에 적용한다.
- JSON은 아래 HTTP 응답과 설명 예시에만 사용한다. WSS의 실제 본문은 Protobuf다.
- `protocol_revision`은 반드시 `2`다. 생략하거나 다른 값을 보내면 거부한다.
- `application_protocol`은 게임 본문의 계약이다. 예시는 `snake-sample.v1`이다.
- 같은 방은 같은 `application_protocol`을 사용한다. 로비·매칭과 접속권에도 이 값을 묶는다.
- Protobuf의 알 수 없는 일반 필드는 건너뛸 수 있다. 해석할 수 있는 `body`가 없으면 거부한다.
- 필드 번호를 재사용하지 않는다. 삭제한 필드 번호와 이름은 `reserved`로 남긴다.

`protocol_revision` 변경은 중계 계약 변경을 뜻한다.
게임 좌표 필드나 게임 사건 코드 변경은 `application_protocol`에서 관리한다.
현재 v1 클라이언트는 v2 엔드포인트에 접속할 수 없다.

## 3. HTTP 배정 응답

새 v2 제어 API가 배정을 반환할 때 아래 정보를 제공한다.
JSON 키는 camelCase이며, Protobuf 원본 필드명은 snake_case다.

```json
{
  "matchId": "m-example",
  "roomId": "r-example",
  "roomInstanceId": "ri-example",
  "nodeId": "node-1",
  "participantId": "p-example",
  "websocketUrl": "wss://relay-1.example.com/v2/relay",
  "subprotocol": "relay.v2",
  "protocolRevision": 2,
  "applicationProtocol": "snake-sample.v1",
  "connectTicket": "<opaque-ticket>",
  "ticketExpiresAt": "2026-10-20T06:00:30Z"
}
```

위 값은 예시다. 접속권 내부 형식은 클라이언트가 해석하지 않는다.
접속권은 플레이어·참가자·방 인스턴스·노드 실행 인스턴스·애플리케이션 계약에 묶인다.
서버는 유효기간과 일회 사용 여부를 원자적으로 검사한다.
서버가 재시작해 같은 주소를 다시 사용해도 이전 방 인스턴스의 권한을 받아들이지 않는다.

클라이언트는 접속권을 URL이나 로그에 넣지 않는다.
새 접속권이 필요하면 HTTPS에서 플레이어 권한을 다시 검사한다.
이미 소멸한 방은 새 방으로 위장해 복구하지 않고 명시적 재입장을 요구한다.
나머지 로비·매칭 HTTP 경로와 요청 본문은 별도 API 명세에서 다룬다.

## 4. 공통 자료형과 번호

| 필드 | 자료형·규칙 |
|---|---|
| ID | `string`, 1–64 ASCII 바이트, `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` |
| 접속권·재접속 토큰 | 해석하지 않는 `string`, 최대 2,048 UTF-8 바이트 |
| `generation` | `uint32`, 1부터 시작하는 참가자의 연결 세대 |
| 클라이언트 `sequence` | `uint32`, 연결별로 증가하는 메시지 번호 |
| 서버 `sequence` | `uint32`, 연결별로 증가하는 서버 메시지 번호 |
| `room_revision` | `uint64`, 방 인스턴스에서 승인한 변경의 증가 번호 |
| `*_unix_ms` | `uint64`, Unix epoch 기준 밀리초 |
| `payload` | `bytes`, 게임이 정의한 바이너리 본문 |
| 사건 `kind` | `uint32`, 1–65,535, 의미는 게임 계약이 정의 |

TypeScript 생성 코드에서 `uint64`는 `bigint`로 취급한다. 일반 `number`로 무조건 변환하지 않는다.
디버그 JSON에서는 `bigint` 값을 십진 문자열로 표시한다.
Protobuf 인코더가 바이트 표현을 담당한다. 애플리케이션이 별도의 바이트 순서나 비트 배치를 구현하지 않는다.

첫 `Connect`의 클라이언트 번호는 `0`이다. 이후 모든 클라이언트 메시지는 `1`부터 증가한다.
서버 번호는 첫 메시지부터 `1`로 시작한다.
양쪽 번호는 실제 소켓 송신 순서대로 부여한다. 교체할 수 있는 대기 상태에 번호를 미리 확정하지 않는다.
중간 번호가 빠져도 허용한다. 같은 연결에서 이전 번호와 같거나 작은 번호는 거부한다.
최대값 뒤에 `0`으로 돌아가지 않는다. 번호를 소진하기 전에 재접속한다.
번호는 WSS 연결 안의 중복·오래된 메시지를 구분하며, 게임 시뮬레이션 틱을 뜻하지 않는다.

## 5. 전체 Protobuf 스키마

아래 계약은 `api/relay/v2/relay.proto`에 구현했다. 생성 코드는 해당 파일을 단일 원본으로 사용한다.
Go 생성 package alias `relayv2`는 wire 계약의 이름이다. 기능 모듈 `internal/relay`와 구분한다.
현재 생성·breaking 검사는 고정한 v2 baseline만 사용한다. [구조 변경 증거](../evidence/v2/2026-10-23-structure-refactor.md)를 참고한다.

```proto
syntax = "proto3";

package relay.v2;

option go_package = "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2;relayv2";

message ClientEnvelope {
  uint32 protocol_revision = 1;
  uint32 sequence = 2;

  oneof body {
    Connect connect = 10;
    StateUpdate state_update = 11;
    PublishEvent event = 12;
    Ping ping = 13;
    Resync resync = 14;
    Leave leave = 15;
  }
}

message ServerEnvelope {
  uint32 protocol_revision = 1;
  uint32 sequence = 2;

  oneof body {
    Welcome welcome = 10;
    StateBatch state_batch = 11;
    RelayedEvent event = 12;
    PeerJoined peer_joined = 13;
    PeerStatusChanged peer_status_changed = 14;
    PeerLeft peer_left = 15;
    Pong pong = 16;
    SyncSnapshot sync_snapshot = 17;
    EventAccepted event_accepted = 18;
    Left left = 19;
    Error error = 20;
  }
}

message Connect {
  oneof credential {
    string connect_ticket = 1;
    string resume_token = 2;
  }
  string application_protocol = 3;
}

message Welcome {
  string room_id = 1;
  string room_instance_id = 2;
  string node_id = 3;
  string participant_id = 4;
  uint32 generation = 5;
  string application_protocol = 6;
  string resume_token = 7;
  uint64 resume_token_expires_unix_ms = 8;
  uint64 room_expires_unix_ms = 9;
  Limits limits = 10;
  RoomSnapshot snapshot = 11;
  bool resumed = 12;
}

message Limits {
  uint32 max_client_message_bytes = 1;
  uint32 max_server_message_bytes = 2;
  uint32 max_state_payload_bytes = 3;
  uint32 max_event_payload_bytes = 4;
  uint32 max_state_updates_per_second = 5;
  uint32 max_events_per_second = 6;
  uint32 state_batch_interval_ms = 7;
  uint32 heartbeat_interval_ms = 8;
  uint32 heartbeat_timeout_ms = 9;
  uint32 resume_grace_ms = 10;
}

message RoomSnapshot {
  uint64 room_revision = 1;
  repeated Peer members = 2;
  repeated PublishedState states = 3;
}

message Peer {
  string participant_id = 1;
  uint32 generation = 2;
  ConnectionStatus status = 3;
  uint64 status_changed_unix_ms = 4;
}

enum ConnectionStatus {
  CONNECTION_STATUS_UNSPECIFIED = 0;
  CONNECTION_STATUS_CONNECTED = 1;
  CONNECTION_STATUS_DISCONNECTED = 2;
}

message StateUpdate {
  bytes payload = 1;
}

message PublishedState {
  string participant_id = 1;
  uint32 generation = 2;
  uint32 client_sequence = 3;
  uint64 room_revision = 4;
  uint64 received_unix_ms = 5;
  bytes payload = 6;
}

message StateBatch {
  uint64 room_revision = 1;
  repeated PublishedState states = 2;
}

message PublishEvent {
  uint32 kind = 1;
  bytes payload = 2;
}

message RelayedEvent {
  string participant_id = 1;
  uint32 generation = 2;
  uint32 client_sequence = 3;
  uint64 room_revision = 4;
  uint64 received_unix_ms = 5;
  uint32 kind = 6;
  bytes payload = 7;
}

message EventAccepted {
  uint32 client_sequence = 1;
  uint64 room_revision = 2;
}

message PeerJoined {
  Peer peer = 1;
  uint64 room_revision = 2;
}

message PeerStatusChanged {
  Peer peer = 1;
  uint64 room_revision = 2;
}

message PeerLeft {
  string participant_id = 1;
  uint32 generation = 2;
  uint64 room_revision = 3;
  PeerLeftReason reason = 4;
}

enum PeerLeftReason {
  PEER_LEFT_REASON_UNSPECIFIED = 0;
  PEER_LEFT_REASON_VOLUNTARY = 1;
  PEER_LEFT_REASON_RESUME_EXPIRED = 2;
  PEER_LEFT_REASON_REMOVED = 3;
}

message Ping {
  uint32 nonce = 1;
  uint64 client_time_unix_ms = 2;
}

message Pong {
  uint32 nonce = 1;
  uint64 client_time_unix_ms = 2;
  uint64 server_received_unix_ms = 3;
  uint64 server_sent_unix_ms = 4;
}

message Resync {
  uint64 last_room_revision = 1;
}

message SyncSnapshot {
  uint32 request_sequence = 1;
  RoomSnapshot snapshot = 2;
}

message Leave {}

message Left {
  uint32 request_sequence = 1;
}

message Error {
  ErrorCode code = 1;
  string message = 2;
  bool fatal = 3;
  uint32 related_client_sequence = 4;
  uint32 retry_after_ms = 5;
}

enum ErrorCode {
  ERROR_CODE_UNSPECIFIED = 0;
  ERROR_CODE_INVALID_MESSAGE = 1;
  ERROR_CODE_PROTOCOL_MISMATCH = 2;
  ERROR_CODE_APPLICATION_PROTOCOL_MISMATCH = 3;
  ERROR_CODE_UNAUTHENTICATED = 4;
  ERROR_CODE_ROOM_UNAVAILABLE = 5;
  ERROR_CODE_SESSION_CONFLICT = 6;
  ERROR_CODE_STALE_SEQUENCE = 7;
  ERROR_CODE_PAYLOAD_TOO_LARGE = 8;
  ERROR_CODE_RATE_LIMITED = 9;
  ERROR_CODE_SLOW_CONSUMER = 10;
  ERROR_CODE_ROOM_ENDED = 11;
  ERROR_CODE_INTERNAL = 12;
}
```

## 6. 메시지별 검사와 전달 규칙

### 6.1 Connect와 Welcome

`Connect`는 연결 후 5초 안에 정확히 한 번 보낸다.
접속권과 재접속 토큰 중 하나가 반드시 있어야 한다. 빈 문자열도 거부한다.
`application_protocol`은 방의 계약과 같아야 한다.
인증 전 다른 메시지와 인증 후 두 번째 `Connect`는 프로토콜 위반이다.

초기 접속권은 기본 30초 동안 사용할 수 있다.
`Welcome`을 받기 전까지 게임 데이터를 전송하지 않는다.
`Welcome`과 참가자 등록·초기 캐시 확보는 같은 방 상태의 일관된 경계를 사용한다.
새 연결에는 `Welcome`보다 다른 메시지가 먼저 도착하지 않게 한다.
첫 메시지가 `Error`라면 접속 실패다.

재접속 토큰은 해당 방 인스턴스·참가자·연결 세대에 묶인다.
토큰의 절대 만료는 방 만료를 넘지 않는다. 복귀 가능 여부는 노드의 유예 상태도 함께 검사한다.
연결이 유지되는 동안 유예 기간이 매번 소진되는 형태로 만들지 않는다.
재접속 성공 시 토큰과 연결 세대를 교체한다.
기존 연결이 아직 살아 있어도 유효한 재접속은 이전 연결을 원자적으로 대체할 수 있다.

재접속 응답을 받기 전에 소켓이 끊겨 새 토큰을 잃을 수 있다.
이 경우 HTTPS에서 플레이어 권한을 재확인하고 같은 참가자의 접속권을 다시 발급한다.
접속권을 재발급하는 절차도 이전 권한과 연결 세대를 확인하며, 임의 참가자 교체를 허용하지 않는다.
새 접속권에는 초기 참여와 기존 연결 교체 중 어느 권한인지 서버가 명시한다.
교체 권한은 예상 연결 세대에 묶고 원자적으로 소비해 동시 재접속 중 하나만 성공하게 한다.

### 6.2 StateUpdate와 StateBatch

`StateUpdate.payload`는 참가자가 소유한 객체들의 독립 전체 상태다.
발신자 ID와 연결 세대는 클라이언트 본문 밖에서 서버가 부여한다.
본문이 비어 있으면 거부한다. 객체가 없는 상태도 애플리케이션이 명시적인 본문으로 표현한다.
서버가 본문을 이해하거나 그 안의 좌표·소유권 주장을 검증하지는 않는다.
수신 클라이언트는 본문보다 서버가 부여한 발신자 식별을 권한의 기준으로 사용한다.

서버가 상태를 승인하면 방의 변경 번호를 증가시키고 마지막 발표 상태를 교체한다.
최대 50ms의 초기 묶음 주기로 다른 활성 참가자에게 전송한다. 자신에게 상태를 반사하지 않는다.
아직 보내지 않은 같은 참가자의 상태가 있으면 최신 값으로 교체한다.
한 배치에는 같은 참가자가 두 번 들어가지 않는다.
`StateBatch.room_revision`은 배치 구성 시점의 방 변경 번호이며 전체 상태를 포함한다는 뜻은 아니다.
목록에 없는 참가자의 상태를 삭제하지 않는다.

SDK는 `(roomInstanceId, participantId, generation, clientSequence)`로 오래된 상태를 제외한다.
방 변경 번호나 상태 번호의 간격을 손실 오류로 해석하지 않는다.
이 상태 번호는 좌표를 계산한 게임 틱이나 서버 판정 결과가 아니다.

### 6.3 Event와 EventAccepted

`kind`는 1–65,535다. 게임 계약이 코드의 의미와 본문을 정의한다.
빈 사건 본문은 게임 계약이 허용하면 사용할 수 있다.
서버가 사건을 승인하면 방의 변경 번호를 증가시키고 수신자별 사건 큐에 넣는다.
동일한 연결에서 보낸 사건 순서는 수신자에게 유지한다.
자신에게 사건을 반사하지 않고 `EventAccepted`를 반환한다.

사건 식별자는 `(roomInstanceId, participantId, generation, clientSequence)`의 조합이다.
`EventAccepted`는 서버의 접수 확인이다. 모든 상대 클라이언트의 수신·적용 완료를 뜻하지 않는다.
느린 수신자의 사건 큐가 차면 그 수신자를 종료한다.
접수 확인을 받지 못한 채 연결이 끊기면 해당 사건의 전달 결과는 미확인이다.

재접속 후 사건을 자동 재전송하지 않는다.
게임이 재시도를 허용하려면 별도의 게임 사건 ID와 중복 처리 규칙을 본문에 넣는다.
서로 다른 발신자의 사건과 교체 가능한 상태 사이에 원자적 순서를 보장하지 않는다.
사망 같은 영구 상태는 다음 전체 상태에도 표현한다.

### 6.4 참가자 변경

새 참가자는 `PeerJoined`, 단절과 복귀는 `PeerStatusChanged`로 알린다.
예약만 하고 아직 인증 접속하지 않은 참가자는 `members`에 넣지 않는다.
참가자의 첫 인증 접속 세대는 `1`이다. 이후 성공한 연결 교체마다 증가하며 되돌아가지 않는다.
복귀 유예 동안 상태는 `DISCONNECTED`다. 참가자는 방에 남지만 캐시는 오래된 발표 상태다.
`status_changed_unix_ms`와 상태 수신 시각으로 이 차이를 표시한다.
명시적 퇴장·유예 만료·운영자 제거는 `PeerLeft`로 알린다.
퇴장 후 같은 식별자를 재사용하지 않는다. 새 참여에는 새 참가자 ID를 발급한다.

연결 세대가 바뀌면 그 참가자의 이전 캐시와 대기 상태를 제거한다.
복귀한 참가자는 자신의 전체 상태를 다시 발표한다. 그전에는 참가자만 있고 상태가 없을 수 있다.
SDK는 마지막 퇴장·세대 정보를 방 참여 동안 보관해, 늦게 도착한 상태가 객체를 다시 생성하지 못하게 한다.
전체 스냅샷을 새 기준으로 적용하면 이 정보를 해당 스냅샷 기준으로 정리할 수 있다.

### 6.5 Ping과 Pong

SDK는 기본 15초 간격으로 Ping을 보낸다. 브라우저가 직접 만드는 애플리케이션 메시지다.
서버는 `nonce`와 클라이언트 시각을 그대로 돌려주고 자체 수신·송신 시각을 붙인다.
`nonce`는 활성 연결에서 응답과 요청을 연결하는 데 사용한다.
SDK는 로컬 단조 시계로 왕복 지연을 계산한다. 클라이언트 시각은 인증과 만료에 사용하지 않는다.
서버가 45초 동안 유효한 인증 메시지를 받지 못하면 연결을 종료한다.
SDK도 Pong 응답을 확인하고, 전송만 계속 성공하는 소켓을 정상 연결로 단정하지 않는다.
단절 판정 뒤 기본 30초 동안 복귀할 수 있다. 방 만료가 더 이르면 방 만료를 따른다.

### 6.6 Resync와 SyncSnapshot

`Resync.last_room_revision`은 클라이언트가 알고 있는 번호다. 서버 권한을 결정하지 않는다.
서버는 해당 번호부터 사건을 재전송하지 않고 새 전체 캐시를 반환한다.
응답의 `request_sequence`는 요청 Envelope의 번호다.

스냅샷 시점의 참가자와 캐시는 같은 방 변경 번호를 기준으로 확보한다.
서버는 스냅샷과 이후 업데이트 사이에 등록·전달 공백이 없게 한다.
스냅샷을 보내기 전, 해당 스냅샷보다 오래된 대기 상태를 제거한다.
제어·사건 FIFO에서 스냅샷의 전송 경계를 정하고, 그 경계를 새 상태 배치가 추월하지 못하게 한다.
SDK는 스냅샷을 적용한 뒤 그 번호 이하의 늦은 상태·참가자 변경 알림을 제외한다.
배치는 각 항목의 변경 번호로 필터링한다. 배치의 최댓값으로 모든 항목을 최신 상태라고 간주하지 않는다.

사건은 캐시에 반영된다고 보장하지 않는다.
스냅샷 번호 이하라는 이유만으로 아직 처리하지 않은 사건을 자동 삭제하지 않는다.
방 캐시에는 참가자의 마지막 발표만 있으므로 공용 월드·결과·몸통 복구는 게임 계약에 의존한다.

### 6.7 Leave와 Left

`Leave`를 처리하면 참가자와 캐시를 즉시 제거한다. 복귀 유예를 적용하지 않는다.
다른 참가자에게 `PeerLeft(VOLUNTARY)`를 알린다.
발신자에게 `Left`를 보낸 뒤 정상 종료한다. `Left.request_sequence`는 요청 번호다.
종료 과정에서 응답이 유실될 수 있다. 명시적 퇴장을 보낸 SDK는 재접속을 시도하지 않는다.

## 7. 초기 상한과 전송 제한

다음 수치는 초기 계약 기본값이다. 성능 측정 결과가 아니며, 실제 뱀 상태 크기로 검증한 뒤 운영값을 확정한다.
서버는 낮춘 실제 한도를 `Welcome.limits`에 알린다. 클라이언트가 협상 없이 상한을 늘릴 수는 없다.

| 항목 | 초기 상한·기본값 |
|---|---:|
| 클라이언트 Envelope 전체 | 16KiB |
| 서버 Envelope 전체 | 128KiB |
| StateUpdate 본문 | 4KiB |
| Event 본문 | 8KiB |
| 상태 발표 | 초당 20회, burst 2회 |
| 사건 발표 | 초당 5회, burst 10회 |
| Ping | 초당 1회, burst 2회 |
| Resync | 초당 1회, burst 1회 |
| 인증 후 전체 수신 | 초당 40메시지, burst 40개 |
| 방 참가자 | 최대 16명 |
| 연결별 제어·사건 큐 | 최대 32개이면서 인코딩한 메시지 합계 256KiB 이하 |
| 단일 쓰기 제한 | 1초 |
| 상태 묶음 주기 | 50ms |

`KiB`는 1,024바이트다. 상한은 경계값을 포함한다.
Envelope 상한에는 Protobuf 메타데이터도 포함한다. 본문만 상한 안에 들어와도 Envelope 상한을 초과하면 거부한다.
인증 전 메시지에는 Connect 형식·토큰 길이·5초 제한과 별도 접속 예산을 적용한다.
캐시는 참가자별 상태 한 개로 제한한다. 전체 참가자의 최악 상태를 포함한 Welcome과 스냅샷이 서버 메시지 상한 안에 들어야 한다.
유효한 방 전체 스냅샷을 만들 수 없는 한도 조합은 서버 시작 또는 방 생성 때 거부한다.

서버는 메시지·참가자·방·노드의 바이트 예산도 검사한다.
위 메시지별 상한이 모든 참가자의 최대 빈도·최대 본문 동시 전송을 허용한다는 뜻은 아니다.
빈도·바이트 초과 시 승인하지 않고 `RATE_LIMITED`를 반환한다. 지속 초과 연결은 종료할 수 있다.
거부한 메시지의 번호를 다시 사용하지 않는다. 새 번호와 최신 상태로 재시도한다.
게임 사건은 재시도 규칙이 있을 때만 다시 보낸다.

## 8. 오류와 종료

`Error.message`는 표시·진단용이다. SDK의 분기는 `code`와 `fatal`을 사용한다.
오류 본문에 토큰·개인 정보·원본 게임 본문을 넣지 않는다.
치명적 오류의 Error 메시지는 가능한 경우에만 보낸다. 닫기 프레임만 받을 수도 있다.

| 오류 | 의미·복구 | 기본 정책 |
|---|---|---|
| INVALID_MESSAGE | 필수 필드 누락·잘못된 메시지 | 연결 종료 |
| PROTOCOL_MISMATCH | 중계 버전 불일치 | 연결 종료, 클라이언트 갱신 |
| APPLICATION_PROTOCOL_MISMATCH | 게임 본문 계약 불일치 | 연결 종료, 올바른 방 선택 |
| UNAUTHENTICATED | 접속권·토큰을 받아들일 수 없음 | 연결 종료, HTTPS에서 권한 재확인 |
| ROOM_UNAVAILABLE | 방 또는 노드 인스턴스를 사용할 수 없음 | 연결 종료, 배정 API 재조회 |
| SESSION_CONFLICT | 초기 접속으로 기존 연결을 덮어쓰려 함 | 연결 종료, 재접속 계약 사용 |
| STALE_SEQUENCE | 같은 연결의 이전 번호 재사용 | 연결 종료, SDK 오류 검사 |
| PAYLOAD_TOO_LARGE | 본문 또는 전체 메시지 초과 | 연결 종료, 크기 축소 |
| RATE_LIMITED | 빈도·바이트 예산 초과 | 비치명적, retry_after_ms 적용 |
| SLOW_CONSUMER | 제어·사건 큐 또는 쓰기 제한 초과 | 연결 종료, 복귀 후 상태 재조회 |
| ROOM_ENDED | 방 종료·만료 | 연결 종료, 재입장 |
| INTERNAL | 서버 내부 실패 | 연결 종료, 지연 후 상태 확인 |

`related_client_sequence`는 원인이 된 클라이언트 메시지 번호다. 특정 요청이 없으면 `0`이다.
인증 단계는 Connect 번호가 `0`이므로 오류 코드와 현재 연결 상태도 함께 해석한다.
`retry_after_ms`는 서버가 제시한 최소 대기 시간이다. 권한 만료를 연장하지 않는다.
접속권 오류는 존재 여부·정확한 실패 원인을 과도하게 노출하지 않는다.

| WebSocket 종료 코드 | 용도 |
|---|---|
| 1000 | 명시적 퇴장·정상 방 종료 |
| 1002 | Protobuf를 해석할 수 없는 프로토콜 위반 |
| 1003 | 텍스트 메시지 수신 |
| 1008 | 인증·버전·필드·순서 위반, 지속적인 제한 초과 |
| 1009 | 메시지 크기 초과 |
| 1011 | 서버 내부 실패 |
| 1012 | 서버 재시작·강제 종료 안내 |
| 1013 | 느린 수신자·일시적인 수용량 부족 |

1006은 관찰할 수 있는 비정상 종료 상태이며 서버가 전송할 닫기 코드가 아니다.
닫기 코드만으로 복귀 가능 여부를 단정하지 않는다. 토큰·방 인스턴스·유예 상태를 확인한다.

## 9. 뱀 게임 본문의 예시

아래 항목은 `application_protocol = snake-sample.v1`에 넣을 수 있는 예시다.
중계 서버가 이해해야 하는 필드는 아니며, Snake.io의 원본 패킷 명세도 아니다.

| 필드 | 예시 자료형 | 의미 |
|---|---|---|
| life_id | uint32 | 부활마다 바뀌는 객체 생명주기 |
| head_x, head_y | float | 게임 좌표 단위의 머리 위치 |
| heading_radians | float | 라디안 단위 방향 |
| speed | float | 게임 좌표 단위/초 |
| body_count | uint32 | 몸통 수 |
| body_spacing | float | 몸통 간격 |
| body_path | 경로 점 배열 | 현재 몸통을 복원할 경로 샘플 |
| is_boosting | bool | 부스트 표시 상태 |
| is_alive | bool | 현재 생존 상태 |

게임 클라이언트는 NaN·무한대·범위 밖 좌표와 과도한 경로 점을 검사한다.
몸통 경로는 상태 본문 4KiB 상한 안에서 복원 가능한 해상도와 최대 길이를 정해야 한다.
길이 상한을 늘리려면 상태 예산과 방 전체 스냅샷 크기를 함께 검증한다.
생성·사망·부활 사건에는 해당 `life_id`를 포함해 이전 생명주기의 사건이 새 뱀에 적용되지 않게 한다.
이 게임 계약과 충돌 판정 주체는 실제 클라이언트 구현 전에 별도로 확정한다.

## 10. 필수 검증 항목

- 동일 픽스처를 Go와 TypeScript에서 양방향 인코딩·디코딩한다.
- uint64의 안전 정수 범위 초과 값과 uint32 경계값도 호환 검사에 포함한다.
- 인증 전 메시지, 두 번째 Connect, 잘못된 게임 계약, 만료·중복 접속권을 거부한다.
- 상태 번호 간격은 허용하고, 중복·역행과 이전 연결 입력은 거부한다.
- 참가자별 최신 상태를 유지하며 다른 참가자의 상태를 덮어쓰지 않는다.
- 사건 접수 확인과 수신자 적용 완료를 혼동하지 않는다.
- 느린 수신자·빈도 제한·메시지 크기 경계를 검사한다.
- 초기 Welcome·Resync와 동시 입퇴장·상태 갱신 사이의 공백을 검사한다.
- 재접속 시 이전 세대 캐시 제거와 새 전체 상태 발표를 검사한다.
- 퇴장 후 늦은 상태가 객체를 다시 생성하지 않는지 검사한다.
- 노드 재시작·잘못된 노드 주소·이전 방 인스턴스의 권한을 검사한다.
- 브라우저와 Android 패키지가 같은 바이너리 메시지를 교환하는지 검사한다.

이 문서의 스키마 구문 검사는 런타임·게임 규칙·전달 보장의 검증과 다르다.
실제 서버와 TS SDK에 적용했다. `client/scripts/check-wire.ts`는 17개 메시지 본문과 uint64 최댓값의 Go→TS→Go 호환을 검사한다.
