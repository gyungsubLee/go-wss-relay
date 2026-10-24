새 실행기는 기존 증거를 보존하면서 인증 제한과 전송 제한을 다시 검사한다.
이 문서는 실행 절차다. 검사 결과는 새 JSON과 보고서에 기록한다.
실행기를 준비한 사실만으로 부하 검사를 통과했다고 판정하지 않는다.

이전 근거는 [`../2026-10-23-live-load/REPORT.md`](../2026-10-23-live-load/REPORT.md)에 있다.
같은 깊이의 새 폴더를 사용하므로 기존 TypeScript import 경로와 Python 루트 계산을 유지한다.
기본 슬롯 200개와 방 상한 512개를 사용한다. 기본 바이트 예산도 유지한다.
`run-config.json`은 준비한 조건을 기록한다. 실행 결과를 뜻하지 않는다.

1. 저장소 루트에서 포트와 기존 프로세스를 확인한다.
   공개 포트는 `19443`, 관리 포트는 `19092`다.
   다른 작업이 해당 포트를 사용하면 그 프로세스를 종료하지 않는다.

2. 기존 자격 정보의 파일 권한과 인증서 유효 기간을 확인한다.
   토큰 파일은 `.cache/live-load-20261023/operator.token`이며 권한은 `0600`이어야 한다.
   인증서와 개인 키는 같은 폴더의 `tls/cert.pem`과 `tls/key.pem`에 있다.
   토큰과 개인 키의 값을 출력하지 않는다.

   ```sh
   stat -f '%Lp %N' .cache/live-load-20261023/operator.token
   openssl x509 -in .cache/live-load-20261023/tls/cert.pem -noout -dates -fingerprint -sha256
   ```

3. 수정 전 진단 서버와 부하 도구를 빌드한다.
   진단 서버의 경로는 `out/auth-rate-diagnosis/wss-relay`다.
   같은 소스의 부하 도구를 `out/auth-rate-diagnosis/relay-load`에 둔다.
   진단 계측을 overlay로 넣었다면 원본과 overlay 파일을 모두 해시에 포함한다.

   ```sh
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/record-bindings.py --tag baseline --binary-dir out/auth-rate-diagnosis --extra-source /absolute/path/to/diagnostic-overlay.json --extra-source /absolute/path/to/diagnostic-source.go
   ```

4. 진단 서버를 시작하고 실제 PID를 보관한다.
   서버 로그는 `baseline-server.log`에 기록한다.

   ```sh
   out/auth-rate-diagnosis/wss-relay \
     --listen 127.0.0.1:19443 \
     --management-listen 127.0.0.1:19092 \
     --operator-token-file .cache/live-load-20261023/operator.token \
     --tls-cert .cache/live-load-20261023/tls/cert.pem \
     --tls-key .cache/live-load-20261023/tls/key.pem \
     --public-url wss://localhost:19443/v2/relay \
     --allowed-origin localhost:5173
   ```

5. 진단 부하를 순서대로 실행한다.
   `SERVER_PID`에는 직접 시작한 서버의 PID를 넣는다.
   SDK 20Hz와 Go 10Hz·60초는 이전에 실패한 조건이다.
   오류가 없는 시도도 보존한다. 이전 실패가 매번 반복된다고 가정하지 않는다.

   ```sh
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/run-sdk.py --hz 20 --tag baseline --server-pid "$SERVER_PID" --binary-dir out/auth-rate-diagnosis
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/run-load.py --suite ten-hz-60 --tag baseline --server-pid "$SERVER_PID" --binary-dir out/auth-rate-diagnosis
   AUTH_PROBE_MODE=observe NODE_EXTRA_CA_CERTS="$PWD/.cache/live-load-20261023/tls/cert.pem" node --disable-warning=ExperimentalWarning --experimental-transform-types docs/evidence/v2/2026-10-24-auth-rate-retest/security-probes.ts https://localhost:19443 docs/evidence/v2/2026-10-24-auth-rate-retest/security-baseline.json
   ```

6. 진단 서버를 종료하고 수정한 소스를 검증한다.
   방·연결·예약 슬롯이 모두 0인지 먼저 확인한다.
   직접 시작한 PID에만 `SIGTERM`을 보낸다.
   부하가 실행 중인 동안 빌드나 검사를 병렬로 실행하지 않는다.

7. 수정한 소스로 서버와 부하 도구를 다시 빌드한다.
   정식 바이너리는 `out/auth-rate-retest-20261024/`에 둔다.
   `record-bindings.py --tag final`로 소스와 바이너리 해시를 고정한다.

   ```sh
   mkdir -p out/auth-rate-retest-20261024
   GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go build -o out/auth-rate-retest-20261024/wss-relay ./cmd/wss-relay
   GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go build -o out/auth-rate-retest-20261024/relay-load ./cmd/relay-load
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/record-bindings.py --tag final
   ```

8. 같은 옵션으로 정식 서버를 시작한다.
   4단계 명령의 실행 파일을 `out/auth-rate-retest-20261024/wss-relay`로 바꾼다.
   서버 로그는 `final-server.log`에 기록한다.
   새 PID를 `SERVER_PID`에 넣는다.

9. 아래 재시험을 순서대로 실행한다.
   각 실행이 끝나면 결과와 정리 상태를 확인한다.
   하위 조건이 실패하면 더 큰 부하를 실행하지 않는다.

   ```sh
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/run-load.py --suite diagnose --tag final --server-pid "$SERVER_PID"
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/run-sdk.py --hz 20 --tag final --server-pid "$SERVER_PID"
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/run-sdk.py --hz 10 --tag final --server-pid "$SERVER_PID"
   python3 docs/evidence/v2/2026-10-24-auth-rate-retest/run-load.py --suite ten-hz-60 --tag final --server-pid "$SERVER_PID"
   NODE_EXTRA_CA_CERTS="$PWD/.cache/live-load-20261023/tls/cert.pem" node --disable-warning=ExperimentalWarning --experimental-transform-types docs/evidence/v2/2026-10-24-auth-rate-retest/security-probes.ts https://localhost:19443 docs/evidence/v2/2026-10-24-auth-rate-retest/security-final.json
   ```

10. 결과를 비교하고 서버를 종료한다.
    정상 부하 기준은 예상하지 않은 오류 0건, p95≤100ms, p99≤200ms, RSS≤512MiB다.
    CPU 표본은 논리 CPU 전체의 70% 이하여야 한다.
    오류가 없더라도 상태·이벤트·하트비트와 세대 변경을 함께 확인한다.
    종료 후 방·연결·예약 슬롯이 0인지 확인한다.

추가 해석 기준

- SDK는 실제 `LobbyClient`와 `RelayClient`를 사용한다. 공개 HTTPS 로그인·매칭·개별 배정을 거쳐 WSS에 연결한다.
- SDK는 200명, 정원 16명, 679바이트, 예열 3초, 본 측정 30초를 유지한다.
- SDK는 추가 참가자 2명을 차단하고, 200명이 집중 재접속한 뒤 5초 동안 전달을 확인한다.
- SDK의 `metricSnapshots`는 재접속 전후, 이벤트 대기 전후, 정리 전후 계측을 기록한다.
- `phaseMetricDeltas`는 각 구간의 `writeErrors` 등을 계산한다. 의도한 종료 구간의 오류도 자동으로 통과시키지 않는다.
- `errorDetails`는 클라이언트 오류의 단계와 경과 시간을 기록한다. 토큰과 배정 본문은 기록하지 않는다.
- Go의 `ten-hz-60`은 200명, 정원 16명, 679바이트, 10Hz, 예열 2초, 본 측정 60초를 사용한다.
- 보안 검사는 미인증 연결을 제한된 수만 열고 첫 `429`에서 관측을 멈춘다. 고정된 128개 연결의 성공을 가정하지 않는다.
- 정식 보안 검사는 관측한 IP별 상한을 선언한 64개와 비교한다. 전역 상한 128개보다 작아야 한다.
- 보안 검사는 슬롯 하나를 반환한 뒤 연속 인증 두 건을 확인한다. 인증 성공 시 슬롯을 반환하지 않으면 실패한다.
- 서로 다른 실제 IP와 신뢰 프록시의 분리는 제품 회귀 검사로 확인한다. 이 보안 검사는 같은 loopback IP만 사용한다.
- 같은 파일 이름이 있으면 Python 실행기는 덮어쓰기를 거부한다. 재실행에는 새로운 `--tag`를 사용한다.
- 같은 호스트의 서버와 발생기가 자원을 공유한다. 인터넷·Linux·Android 성능으로 일반화하지 않는다.

Go 실행기의 `metricDelta`는 본 측정 구간만 판정한다.
`setupAndWarmupMetricDelta`, `postMeasurementMetricDelta`, `lifecycleMetricDelta`는 다른 구간의 원시 오류 수를 보존한다.
계획한 종료 구간에서 오류가 늘어도 전체 무오류로 기록하지 않는다.
