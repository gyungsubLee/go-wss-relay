# 공개 제어 API v2

갱신일: 2026-10-23. 단일 원본은 `internal/control`의 라우트·검증과 `client/src/relay/api.ts`다.
`control.Manager`는 로비·FIFO·배정·만료와 잠금을 소유한다. HTTP Server는 입력·인증·응답을 담당한다.
응답 값은 내부 맵·슬라이스와 분리한다. JSON 인코딩과 원격 예약은 Manager 잠금 밖에서 실행한다.
접두사는 `/v2/api`, JSON 키는 camelCase다. 요청 JSON은 정확한 필드 집합을 요구한다.
게스트 발급 이외에는 `Authorization: Bearer <player-token>`이 필요하다.
토큰·접속권을 URL·로그·영구 브라우저 저장소에 넣지 않는다.

## 인증

| 요청 | 본문 | 성공 응답 |
|---|---|---|
| POST /sessions | `{}` | 201, participantId·token·expiresAt·expiresInSeconds |
| POST /sessions/refresh | `{}` | 200, 같은 참가자의 새 토큰과 유효기간 |

토큰 수명은 15분이다. SDK는 expiresInSeconds의 75% 시점에 갱신한다.
만료 전에 갱신하지 못하면 재인증·재입장이 필요하다. 인증이 해제된 사용자를 토큰 내용만으로 복원하지 않는다.

## 로비

| 요청 | 본문 또는 쿼리 | 성공 응답 |
|---|---|---|
| POST /lobbies | capacity: 2..16, applicationProtocol: ID, visibility: public 또는 private | 201 Lobby |
| GET /lobbies | applicationProtocol 필수, cursor 선택, limit: 1..50 선택 | 200, lobbies·nextCursor |
| GET /lobbies/{id}/preview | 없음 | 200, id·revision·capacity·participantCount·visibility·applicationProtocol |
| GET /lobbies/{id} | 없음, 참가자만 | 200 Lobby |
| POST /lobbies/{id}/join | revision | 200 Lobby |
| POST /lobbies/{id}/ready | revision·ready: bool | 200 Lobby |
| POST /lobbies/{id}/leave | revision | 200 Lobby, 마지막 참가자라면 204 |
| POST /lobbies/{id}/start | revision, 소유자만 | 204 |
| GET /lobbies/{id}/assignment | 없음 | 200 Assignment |
| POST /lobbies/{id}/assignment | `{}` | 200, 새 접속권의 Assignment |

Lobby 필드: id, revision, ownerId, participants, capacity, status, visibility, ready, allocating, allocationPending.
status는 open·matched·ended다. 참가 전 preview는 참가자 목록과 접속권을 노출하지 않는다.
private 로비는 공개 목록에 포함하지 않는다. 코드를 아는 인증 사용자는 preview 후 참가할 수 있다.
공개 목록은 ID 오름차순이며 페이지 크기는 최대 50이다. 변경되는 목록의 고정 스냅샷은 보장하지 않는다.
applicationProtocol은 `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` 형식이다.

revision은 1부터 증가한다. 충돌하면 409를 받고 로비를 다시 조회한다.
참가·퇴장 시 모든 ready를 초기화한다. 소유자가 나가면 남은 참가자 중 먼저 참가한 사람이 소유자가 된다.
정원 충족·전원 준비 상태에서 소유자가 시작한다. matched 상태의 소유자 시작 재시도는 204다.
예약 응답이 불확실하면 allocationPending=true로 참가자 구성과 준비 상태를 고정한다.
같은 revision·로비로 시작을 재시도한다. 별도 방을 만들거나 참가자를 바꾸지 않는다.
matched 이후의 종료·퇴장은 WSS 생명주기를 따른다.

Assignment 필드: matchId, roomId, roomInstanceId, participantId, nodeId, websocketUrl,
applicationProtocol, protocolRevision=2, subprotocol=relay.v2, connectTicket, ticketExpiresAt, expiresAt.
matchId는 현재 roomId와 같다. 접속권은 해당 참가자 하나에 한정하며 최초 사용 수명은 최대 30초다.
GET 응답의 접속권이 이미 소비됐거나 만료됐으면 POST로 다시 발급받는다.
재발급은 이전 미사용 접속권을 폐기한다. 새 접속권 소비 시 기존 연결 세대를 교체한다.
소멸한 방의 배정 조회는 410이며 새 방으로 자동 대체하지 않는다.

## FIFO 매칭

| 요청 | 본문 | 성공 응답 |
|---|---|---|
| POST /matchmaking | capacity: 2..16, applicationProtocol: ID | 201 MatchTicket |
| GET /matchmaking/{id} | 없음, 본인 표만 | 200 MatchTicket |
| DELETE /matchmaking/{id} | 없음, 아직 묶이지 않은 본인 표만 | 204 |

MatchTicket은 id, status와 배정 완료 시 assignment를 가진다. status는 queued·matched·ended다.
같은 applicationProtocol·capacity를 FIFO로 묶는다. 대기표 수명은 2분이다.
그룹을 형성하면 모든 티켓에 형성 시각부터 2분의 같은 기한을 적용한다. 배정 중에는 그룹과 티켓을 함께 보존한다.
배정 성공 후에는 배정 만료를 적용한다. 실패 후 재시도는 형성 기한을 늘리지 않는다.
묶인 그룹은 같은 방 ID와 참가자 구성으로 예약을 재시도한다. 그룹 형성 이후 취소는 409다.
결과 불명·기존 노드 unhealthy·빈 503은 구성을 고정한다. 노드 JSON의 `allocation_failed`는 확정 거부다.
확정 거부는 수동 로비 수정을 다시 허용한다. 종료 후 늦은 성공은 그룹을 되살리지 못한다.
배정 후 재접속권은 Assignment.roomId의 로비 assignment POST로 발급받는다.

## 오류와 수명

400은 본문·쿼리·ID 오류, 401은 인증 오류, 403은 권한·Origin 오류다.
404는 대상 없음, 409는 버전·멤버십·준비 충돌, 410은 방 종료다.
429는 요청 제한, 503은 용량·노드·할당 불가다. SDK는 일시 오류와 영구 오류를 구분한다.
본문의 오류 형식은 httpx.WriteError를 따른다. HTTP 성공은 WSS 접속 성공이나 상대 이벤트 적용 완료를 뜻하지 않는다.

게스트 발급은 IP별 20회/s·버스트 40회다. 전체 API는 2000회/s·버스트 4000회·동시 처리 128개다.
공유 NAT와 공격 차단의 절충값이며 운영 트래픽에 맞춰 다시 검증한다.
Origin 허용은 HTTP CORS와 WSS에서 각각 검사한다. 사전 요청은 플레이어 인증보다 먼저 처리한다.
대기 로비 수명은 30분, 매치 수명은 기본 30분이다. 활성 방·로비·큐는 메모리 상태다.
배정기 재시작은 노드 활성 방을 복원한다. 대기 로비·대기표는 복원하지 않는다.

관리 API는 공개 API와 별도 포트·별도 운영자 토큰을 사용한다.
예약·노드 등록·드레이닝·키 배치는 [배포 절차](../../infrastructure/README.md)를 따른다.
