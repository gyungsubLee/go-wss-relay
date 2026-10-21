# WSS v2 배포 절차

이 설정은 coordinator 하나와 relay 노드 둘 이상을 실행한다. 기존 infrastructure/ops의 UDP 설정과 구분한다.
공식 Caddy 2.11.7의 adapt·validate는 통과했다. 실제 Linux 호스트 기동·배포는 실행하지 않았다.

## 호스트와 네트워크

- 배정기: 공개 HTTPS `/v2/api/*`, 루프백 관리 포트 8091.
- 각 중계 노드: 고유 공개 도메인의 WSS `/v2/relay`, 루프백 관리 포트 8091.
- 노드 관리 프록시: 사설 인터페이스의 HTTPS. 배정기 사설 IP만 허용한다.
- 인터넷에는 80/443만 연다. 관리 포트와 기존 UDP 포트는 공개하지 않는다.
- 배정기와 노드의 시스템 시간을 동기화한다. 방 잔여 TTL은 노드 응답의 상대 시간을 적용한다.

Caddyfile.coordinator와 Caddyfile.relay는 서로 다른 호스트용이다.
각 Caddy 환경 변수의 example.com 주소와 사설 IP를 실제 값으로 바꾼다.
노드 관리 프록시의 `tls internal` 루트 인증서를 배정기에 안전하게 복사한다.
노드마다 루트가 다르면 두 루트를 하나의 PEM 묶음으로 만들거나 노드별 caFile을 지정한다.
검증을 끄거나 시스템 전체에 개발 인증서를 신뢰시키지 않는다.

## 설치와 실행

1. Linux 대상 바이너리를 빌드해 `/usr/local/bin/wss-relay`에 설치한다.
2. 권한 없는 wss-relay 계정을 만들고 `/etc/wss-relay`에 설정을 설치한다.
3. 기존 `infrastructure/scripts/install-operator-token.py --help`로 토큰 설치 방법을 확인한다.
4. 배정기와 노드별로 서로 다른 32바이트 base64url 운영자 토큰을 생성한다. 파일 읽기는 서비스 계정만 허용한다.
5. nodes.example.json을 nodes.json으로 복사하고 URL·토큰 경로·CA 경로를 설정한다.
6. 역할별 env.example을 coordinator.env 또는 relay.env로 설치한다. 인수 경로에는 공백을 사용하지 않는다.
7. traffic.json과 wss-relay@.service를 설치한다. 중계 노드 둘을 먼저 시작하고 배정기를 시작한다.
8. 대상 호스트의 `caddy validate --config <설정>`과 `systemd-analyze verify <유닛>`을 통과한 뒤 적용한다.

서비스 실행 예: `systemctl enable --now wss-relay@relay`.
배정기 실행 예: `systemctl enable --now wss-relay@coordinator`.
공개 주소는 wss, 관리 URL은 사설 HTTPS를 사용한다. 운영자 토큰을 브라우저에 전달하지 않는다.
기본 샘플은 Caddy가 같은 호스트의 루프백에서 연결하므로 `--trusted-proxy 127.0.0.1/32`만 신뢰한다.
Caddy 앞에 추가 프록시가 있으면 검증한 전달 경로만 구성한다. 임의 X-Forwarded-For를 신뢰하면 IP 제한을 우회할 수 있다.

## 전송량과 증설

CLI 기본 상한은 512개 방·200개 참가자 슬롯이며, 방당 최대 정원은 16명이다.
이는 32개 몸통 경로·679바이트 상태, 방당 16명·20 Hz의 200명 프로필을 측정한 초기 설정이다. 클라우드 호스트의 성능 보장 값은 아니다.
`--traffic-config`는 노드 수신 8 MiB/s·송신 64 MiB/s, 방 수신 512 KiB/s·송신 4 MiB/s를 기본으로 적용한다.
0 또는 생략 필드는 기본값을 사용한다. 음수·최소 한도 미만·알 수 없는 키·추가 JSON 문서는 거부한다.
Protobuf 바이트 예산에는 WebSocket·TLS·TCP 비용이 포함되지 않는다.

배정기는 5초 간격으로 노드를 확인한다. 15초 넘게 오래된 보고는 신규 배정에 사용하지 않는다.
슬롯·예약·송신량·큐 지연·Go 메모리·CPU 추정값으로 입장을 제한한다.
CPU 최초 측정은 구간 측정이 아니므로 준비될 때까지 신규 슬롯을 16개로 제한한다.
바이트 예산 80%, 큐 p95 100 ms, Go 메모리 512 MiB, Go CPU 추정 70%를 배정 차단 기준으로 사용한다.
Go CPU 추정값은 OS CPU 사용률과 다르다. OS CPU·RSS·NIC와 실제 기기 지연을 함께 관찰한다.

증설 시 nodes.json에 새 노드를 추가한 뒤 배정기 관리 API `POST /v2/nodes/reload`에 `{}`를 보낸다.
등록은 운영자가 지정한 파일만 읽는다. 요청 본문으로 임의 관리 URL이나 토큰을 업로드하지 않는다.
증설은 새 방에만 적용한다. 진행 방은 원래 노드에 남는다.

## 드레이닝과 장애

배정기 관리 API `POST /v2/nodes/{id}/drain`에 `{"draining":true}`를 보낸다.
기존 방과 유예 내 복귀는 유지하고 신규 예약을 차단한다. 마지막 방이 사라지면 노드가 정상 종료한다.
systemd Restart=on-failure이므로 정상 배수 종료는 자동 재시작하지 않는다.
관리 API는 운영자 Bearer 토큰을 요구한다. 토큰은 명령 인수·URL·로그에 노출하지 않는다.

관리 명령은 해당 호스트의 루프백에서 실행하거나 인증한 SSH 연결을 사용한다.
배정기 재시작 시 같은 운영자 키와 노드 목록을 사용한다. 노드가 보유한 활성 방에서 배정을 복원한다.
대기 로비·아직 묶이지 않은 매칭표는 메모리에만 있어 재시작 후 다시 만들어야 한다.
중계 노드가 종료되면 해당 방의 게임 상태는 소실된다. 새 노드가 기존 방인 것처럼 복구하지 않는다.
배정기는 단일 작성자다. 같은 노드 집합을 대상으로 배정기를 동시에 둘 이상 실행하지 않는다.

## 확인할 지표

노드 관리 `GET /v2/status`, `GET /v2/node`와 배정기 `GET /v2/nodes`를 수집한다.
큐 p95/p99는 버킷 상한이다. 원시 종단 지연과 혼동하지 않는다.
느린 연결은 쓰기 시간제한 또는 큐 상한으로 분리한다. 사건의 상대 적용 완료는 서버 접수 확인과 다르다.
키·토큰·접속권을 로그에 기록하지 않는다. 운영 모니터링·경보 배선은 환경별 배포 작업이다.

검증된 로컬 시나리오는 `client/scripts/check-cluster.ts`와 docs/evidence/v2에 기록한다.
운영 검증은 공개 인증서·DNS·방화벽·관리 CA·실제 호스트 부하·Android 실기기를 포함해야 한다.
