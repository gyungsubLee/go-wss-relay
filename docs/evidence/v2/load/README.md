# TLS 전송량 검증

2026-10-22, MacBookPro18,1·10 논리 CPU·16 GiB, 같은 호스트의 별도 Go 서버·부하 프로세스로 측정했다.
최종 서버와 부하 도구는 Go 1.26.10으로 빌드했다. profile.json에 소스 커밋과 두 실행 파일의 SHA-256을 고정했다.
클라이언트는 실제 TLS 검증을 수행했다. 테스트 CA 신뢰를 명시했고 인증서 검증을 끄지 않았다.
인터넷·Android 기기의 지연·성능을 측정한 결과가 아니다.

profile.json은 측정 전에 고정한 목표다. 단계별 증가를 적용했고 실패하면 다음 단계로 진행하지 않도록 도구가 중단한다.
상태 100바이트의 100·500·1000명 × 정원 2·8·16명 × 10·20 Hz, 18개 조건이 모두 통과했다.
측정 구간은 예열 2초 뒤 10초다. 종단 지연은 1/16 표본, 최대 65536개 이동 표본이다.
연결 설정은 기본 IP별 200회/s 제한 아래에서 순차적으로 수행했다.
측정 노드만 최대 슬롯을 1024로 높였다. 제품 기본 상한 200명과 구분한다.

1000명·정원 16명·20 Hz의 실제 결과는 c1000-n16-f20.json에 있다.
노드 Protobuf 송신 약 37.1 MB/s, RSS 약 105 MiB, OS CPU 약 131%였다.
OS CPU 100%는 한 코어 기준이며, 이 장비 전체 10코어의 13.1%에 해당한다.
Protobuf 바이트에는 프레임·TLS·TCP 오버헤드가 포함되지 않는다.

## 실제 뱀 크기와 예외 조건

- snake-c200-n16-f20.json: 679바이트 전체 상태, 200명·정원 16명·20 Hz, p99 약 49.8 ms, 송신 약 41.4 MB/s.
- snake-max-c200-n16-f10.json: 1127바이트 최대 경로 상태, 200명·정원 16명·10 Hz, p99 약 50.6 ms.
- slow-c100-n8-f20.json: 방마다 수신자 하나를 500 ms 간격으로 읽도록 지연. 정상 수신자의 p99 약 53.8 ms.
- reconnect-100.json: 실제 TS SDK 100명 전원 복귀. 최장 약 111 ms.
- reconnect-1000.json: 실제 TS SDK 1000명 전원 복귀. p99 약 5.5초, 최장 약 5.8초.

느린 수신자의 소켓이 반드시 막혔다고 가정하지 않는다. 실제 쓰기 차단·시간제한은 별도 Go 회귀 검사가 검증한다.
서버 내부 큐·쓰기 p95/p99는 관리 Stats의 히스토그램 버킷 상한이며, 종단 지연 표본과 다르다.
Go 메모리·GC와 별도 OS CPU·RSS를 함께 기록했다. 공유 장비의 다른 작업과 하드웨어 차이는 결과에 영향을 줄 수 있다.

네트워크 지연·연결 손실 검사는 ../2026-10-22-network-faults.json에 기록했다.
TLS를 그대로 통과시키는 TCP 프록시가 방향별 청크에 100 ms를 추가했다.
사건 5건은 약 203~213 ms에 도착했고, 연결을 강제로 끊은 뒤 약 859 ms에 g2로 복귀했다.
이는 통제한 TCP 지연·연결 손실이며 무선 망의 IP 패킷 손실률·실기기 검증을 대신하지 않는다.

## 재현

out/wss-relay와 out/relay-load를 빌드하고, TLS 노드를 localhost:9443·관리 포트 8092에 실행한다.
.cache/wss-demo.token과 .cache/dev-tls/cert.pem이 필요하다. 키·토큰은 저장소에 넣지 않는다.

```sh
python3 scripts/check-load-matrix.py --server-pid <대상-노드-PID>
NODE_EXTRA_CA_CERTS="$PWD/.cache/dev-tls/cert.pem" node --experimental-transform-types client/scripts/check-reconnect.ts 1000
NODE_EXTRA_CA_CERTS="$PWD/.cache/dev-tls/cert.pem" node --experimental-transform-types client/scripts/check-network.ts
```

재측정 시 실제 뱀 본문 크기·주기·정원과 클라우드 NIC 예산에 맞춰 프로필을 고정한다.
기기 수용량과 인터넷 지연은 해당 환경에서 별도로 확정한다.

2026-10-23 구조 변경 후에도 위 재현 스크립트 경로는 유지한다. 전체 부하를 다시 실행하지 않았다.
현재 기능 검사와 남은 조건은 [구조 변경 증거](../2026-10-23-structure-refactor.md)에 있다.
