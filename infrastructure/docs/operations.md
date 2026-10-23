# WSS 배포와 복구

호스트 하나에서 인스턴스 하나만 실행한다. `standalone`, `coordinator`, `relay` 중 하나를 선택한다.
배포 도구는 역할 변경을 거부한다. 역할을 바꾸려면 기존 서비스를 종료하고 설정과 비용 장부를 운영자가 먼저 확인한다.

## 수동 준비 조건

대상은 Ubuntu 24.04의 Python 3.12 이상, systemd, Caddy다. `bash`, `flock`, `curl`, `diff`, `install`도 필요하다.
서비스 계정과 디렉터리, DNS, 시간 동기화, 인증서, 방화벽은 운영자가 준비한다. 이 저장소는 호스트 자동 설정 도구를 제공하지 않는다.
배포 스크립트와 release archive는 검토한 CI 결과물만 사용한다. 체크섬은 손상을 찾으며 발행자 인증을 대신하지 않는다.

1. 로그인할 수 없는 `wss-relay` 사용자와 기본 그룹을 만든다.
2. `/opt/wss-relay/releases`와 `/etc/wss-relay`를 root 소유 0755로 만든다.
3. `/etc/wss-relay/secrets`를 root 소유 0711로 만든다.
4. `/var/lib/wss-relay`를 root 소유 0700으로 만든다.
5. `/etc/caddy`와 배포판 Caddy 유닛 `/lib/systemd/system/caddy.service`가 있는지 확인한다.
6. 선택한 역할의 `config/INSTANCE.env.example`을 `/etc/wss-relay/INSTANCE.env`로 복사한다.
7. `config/caddy.env.example`을 `/etc/wss-relay/caddy.env`로 복사하고 도메인·사설 IP를 수정한다.
8. `config/traffic.env.example`을 `/etc/wss-relay/traffic.env`로 복사하고 인스턴스·외부 NIC·월 한도를 수정한다.
9. 참조하는 `nodes.json`, CA PEM, 초당 예산 `traffic.json`을 설치한다.

설정과 CA 파일은 root 소유, 그룹 `wss-relay`, 0640을 사용한다. 모든 부모 디렉터리는 서비스 계정의 통과를 허용해야 한다.
토큰은 `wss-relay` 소유 0600이어야 한다. 토큰 파일의 심볼릭 링크와 하드 링크를 허용하지 않는다.
Caddy는 배포판 기본 유닛을 사용한다. 별도 Caddy 유닛·drop-in은 수동 통합 후 검증해야 하며 이 배포 형식에서는 지원하지 않는다.
배포 도구가 관리하는 `wss-relay.conf` 외에 Caddy drop-in을 설치하지 않는다.
이전에 설치한 서비스, 타이머, 프록시가 남아 있다면 운영자가 먼저 중지한다. 이전 운영 경로를 새 배포와 동시에 실행하지 않는다.

공개 포트는 80/443이다. 앱의 8090/8091은 루프백에만 바인딩한다.
relay 관리 프록시는 지정한 사설 IPv4에만 바인딩한다. 방화벽은 coordinator 사설 IP에서 오는 관리 연결만 허용한다.
각 노드의 Caddy 내부 CA를 coordinator에 안전하게 전달한다. `caFile`에는 그 노드의 CA PEM을 지정한다.
CA 검증을 끄지 않는다. CA 파일 파싱 성공은 실제 인증서의 호스트명·신뢰·연결 성공을 보장하지 않는다.

## 지원하는 설정 형식

`INSTANCE.env`는 주석·빈 줄과 `RELAY_ARGS=...` 한 줄만 허용한다.
인수는 `--flag value` 쌍이다. 파일 경로는 절대 경로만 허용한다. 인용부호, 줄 연결, 셸 확장, 공백이 있는 경로, `--flag=value`는 지원하지 않는다.
단일 값 플래그를 중복 지정하면 거부한다. `--trusted-proxy`와 `--allowed-origin`은 반복할 수 있다.
스크립트는 env를 `source`나 `eval`로 실행하지 않는다. 이 제한은 배포 형식이며 앱 CLI 전체의 제한이 아니다.

- `--role`은 배포의 INSTANCE와 같아야 한다.
- `--listen`은 `127.0.0.1:8090`, `--management-listen`은 `127.0.0.1:8091`이다. 생략해도 같은 기본값을 사용한다.
- 앱 TLS 플래그는 지원하지 않는다. Caddy가 TLS를 종료하고 평문 루프백으로 전달한다.
- `standalone`과 `relay`의 `--public-url`은 Caddy 도메인의 `wss://DOMAIN/v2/relay`와 같아야 한다.
- 신뢰 프록시는 루프백 IP/CIDR만 지원한다. `/0`은 거부한다.
- 노드 목록은 중복 ID 없는 HTTPS/WSS 노드 1-64개다. JSON의 중복 키, null, 알 수 없는 필드는 거부한다.
- 초당 예산 JSON은 정확한 필드명과 정수만 허용한다. 0은 앱 기본값을 사용한다.
- 수용량 플래그는 모든 배포 역할에서 중계 서버의 유효 범위를 적용한다. 앱 coordinator CLI보다 좁은 조건이다.

Caddy 값은 `/etc/wss-relay/caddy.env`에서 읽는다. `standalone`은 `STANDALONE_DOMAIN`, coordinator는 `COORDINATOR_DOMAIN`을 요구한다.
relay는 `RELAY_DOMAIN`, `NODE_CONTROL_DOMAIN`, `NODE_PRIVATE_IP`, `COORDINATOR_PRIVATE_IP`를 요구한다.
IP는 사설 IPv4다. Caddy 도메인은 소문자 DNS 호스트명이며 포트·와일드카드·URL을 허용하지 않는다.
배포 도구는 변수를 실제 값으로 치환한 `/etc/caddy/Caddyfile`을 설치한다. Caddy 재시작은 앱의 환경 변수에 의존하지 않는다.

## 토큰 설치

운영자 토큰을 명령 인수·URL·로그에 기록하지 않는다. 노드마다 별도 키를 사용한다.

```sh
sudo infrastructure/scripts/install-operator-token.py \
  --directory /etc/wss-relay/secrets \
  --service-user wss-relay \
  --filename node-a.token
```

기본 디렉터리는 `/etc/wss-relay/secrets`, 계정은 `wss-relay`, 파일 이름은 `operator-token`이다.
이름은 영문자·숫자로 시작하는 단일 basename이며 `._-`를 포함할 수 있다. 최대 길이는 128자다.
설치기는 32바이트 토큰을 43자의 canonical base64url로 저장한다. 기존 유효 토큰은 보존하며 잘못된 토큰을 교체하지 않는다.
새 파일은 임시 파일, 파일 fsync, 배타적 link, 디렉터리 fsync 순서로 설치한다. 토큰 값은 출력하지 않는다.

## 월별 NIC 비용 가드

`/etc/wss-relay/traffic.json`은 Protobuf 바이트/초 예산이다. WebSocket·TLS·TCP 비용을 포함하지 않는다.
`/var/lib/wss-relay/traffic.json`은 외부 NIC의 월별 RX+TX 바이트 장부다. 두 파일은 경로·단위·보존 목적이 다르다.
가드는 매분 표본을 저장한다. 시작 전 검사와 배포 전 검사도 최신 카운터를 누적한다.
경고·중지 한도는 제공자의 실제 요금제와 호스트 전체 사용량을 기준으로 운영자가 정한다. 최초 표본은 부팅 이후 바이트를 포함한다.

신규 호스트에서만 다음 명령을 한 번 실행한다. 값은 `traffic.env`와 같아야 한다.

```sh
sudo env WSS_INSTANCE=standalone TRAFFIC_INTERFACE=eth0 \
  TRAFFIC_WARN_BYTES=1000000000000 TRAFFIC_STOP_BYTES=2000000000000 \
  python3 infrastructure/scripts/traffic_budget.py --initialize
```

기존 장부·중지 표식·이전 장부 디렉터리가 있으면 초기화를 거부한다. 이전 장부를 삭제하거나 이름만 바꿔 이 검사를 우회하지 않는다.
기존 호스트는 제공자 전송량과 과금 기간을 먼저 확인하고 별도 회계 전환을 수행한다. 자동 마이그레이션은 제공하지 않는다.

장부 누락·손상, 부팅 ID 변경, NIC 변경, 카운터 감소, 월 역행, 저장 실패, 한도 초과는 서비스를 차단한다.
가드는 선택한 `wss-relay@INSTANCE.service`와 Caddy를 중지하고 runtime mask를 적용한다.
인스턴스 설정이 없거나 잘못되면 지원하는 세 인스턴스와 Caddy를 모두 차단한다.
`traffic-stopped` 표식은 월 변경·프로세스 재시작·호스트 재시작 후에도 유지한다.
두 서비스의 시작 전 검사는 표식을 확인한다. 시작 전 검사 자체는 systemctl을 호출하지 않아 시작 작업의 교착을 피한다.
저장 장치가 고장 나면 표식을 저장하지 못할 수 있다. 이때도 서비스 중지를 시도하며 운영자의 저장 장치 복구가 필요하다.

복구할 때는 제공자 사용량과 NIC·부팅 ID·월을 확인한다. 서비스를 중지한 상태에서 장부를 보수적으로 조정하고 표식을 제거한다.
선택한 서비스와 Caddy의 runtime mask를 해제한 뒤 시작 전 검사를 통과시킨다. 단순히 새 달이라는 이유로 표식을 제거하지 않는다.

## 패키징과 배포

```sh
infrastructure/scripts/package-release.sh /tmp/wss-release.tar.gz
sudo infrastructure/scripts/deploy-backend.sh /tmp/wss-release.tar.gz FULL_40_CHARACTER_COMMIT_SHA relay
```

패키징은 저장소가 고정한 Go 1.26.10으로 Linux amd64 바이너리를 만든다. 기존 출력 파일을 덮어쓰지 않는다.
배포는 root 권한과 소문자 40자리 커밋 SHA를 요구한다. 동시에 하나만 실행할 수 있다.
archive는 최대 30개 항목, 해제 크기 150 MiB다. 절대 경로·상위 경로·링크·장치·특수 모드와 체크섬이 없는 파일을 거부한다.
같은 SHA에 다른 바이트를 배포할 수 없다. 패키지에는 live 설정과 비밀정보를 넣지 않는다.

배포는 env·참조 파일·권한·Caddy·systemd·비용 가드를 검사한 뒤 `current`를 원자적으로 전환한다.
첫 설치의 systemd 검사는 검증 중인 바이너리와 guard 경로를 사용한다. 바이너리를 가상의 dry-run 모드로 실행하지 않는다.
실행 경로는 `/opt/wss-relay/releases/SHA/wss-relay`와 `/opt/wss-relay/current/wss-relay`다.
시작 후 `/healthz`의 200, `/readyz`의 204, 관리 인증의 401을 확인한다. 중계 역할은 WebSocket 경로의 426도 확인한다.
비용 가드를 다시 실행한 뒤 Caddy를 시작하거나 다시 읽는다. 공개 DNS·TLS·사설 경로·실제 부하는 별도 운영 검사다.

실패하면 이전 바이너리, systemd 유닛, Caddy 설정, 가드 파일을 복원한다. 복원 후 상태 검사가 실패하면 두 서비스를 중지한다.
첫 설치가 실패하면 인스턴스와 Caddy를 중지하고 `current`를 제거한다.
토큰·CA·nodes·초당 예산 파일은 전환 실패와 롤백에서 보존한다. 비용 장부는 복원·삭제·감소시키지 않는다.
설정 검사 실패는 장부를 변경하지 않는다. 비용 검사까지 진행하면 측정한 바이트를 저장하거나 중지 표식을 남길 수 있다.
배포 중에도 독립 타이머가 장부를 갱신할 수 있다. 이 갱신을 롤백으로 지우지 않는다.

## 재시작과 메모리 상태

중계 노드 재시작은 그 노드의 진행 매치를 끝낸다. 롤백은 메모리 게임 상태를 복구하지 못한다.
coordinator는 같은 키와 노드 목록으로 살아 있는 노드의 방을 확인한다. 대기 로비와 미완성 매칭표는 다시 만들어야 한다.
같은 노드 집합의 coordinator는 한 개만 실행한다.
드레이닝은 신규 배정을 막고 기존 방을 기다린다. 정상 종료 후 `Restart=on-failure`는 자동 재시작하지 않는다.

## 로컬 검사

```sh
sh -n infrastructure/scripts/check-traffic-budget.sh
bash -n infrastructure/scripts/check-deployment.sh infrastructure/scripts/deploy-backend.sh infrastructure/scripts/package-release.sh
python3 infrastructure/scripts/tests/operator-token.py
python3 infrastructure/scripts/tests/traffic-budget.py
python3 infrastructure/scripts/tests/deploy-backend.py
```

검사는 임시 root와 명령 대체를 사용한다. 실제 호스트의 systemctl·Caddy를 실행하지 않는다.
실제 Ubuntu 기동, 공개 인증서, 관리 CA 신뢰, 방화벽, 장애 복구, 기기 검사는 운영 환경에서 별도로 수행한다.
