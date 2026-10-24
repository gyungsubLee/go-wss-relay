# 종료 뒤 쓰기 오류 재시험

이 문서는 재시험 절차다. 실행 결과는 [`REPORT.md`](REPORT.md)와 [`runtime-verification.json`](runtime-verification.json)에 있다.
수정 전후 결과와 실패를 이 폴더에 보존했다.

기준은 [`2026-10-24-auth-rate-retest/REPORT.md`](../2026-10-24-auth-rate-retest/REPORT.md)의 종료 뒤 쓰기 오류 139건이다.
`run-load.py --suite shutdown`은 그 오류가 나온 6개 조건만 같은 순서로 실행한다.
참가자 수·방 정원·Hz·상태 바이트·측정 초·느린 읽기 ms는 다음과 같다.

| 참가자 | 정원 | Hz | 상태 바이트 | 측정 초 | 느린 읽기 ms | 이전 오류 |
|---:|---:|---:|---:|---:|---:|---:|
| 200 | 16 | 20 | 100 | 15 | 0 | 6 |
| 100 | 16 | 10 | 100 | 10 | 0 | 1 |
| 200 | 16 | 10 | 679 | 30 | 0 | 12 |
| 200 | 16 | 10 | 1127 | 30 | 0 | 106 |
| 100 | 8 | 10 | 679 | 15 | 500 | 12 |
| 200 | 16 | 10 | 679 | 60 | 0 | 2 |

예열은 2초다. 느린 수신자는 방마다 한 명이며, 메시지 하나를 읽은 뒤 500ms를 기다린다.
SDK 조건은 200명·정원 16명·679바이트다. 20Hz와 10Hz를 각각 30초 측정한다.
SDK 예열은 3초이며, 재접속 후 5초도 검사한다.

## 실행 전

1. 저장소 루트에서 `python3 docs/evidence/v2/2026-10-24-shutdown-retest/check-harness.py`를 실행한다. `harness checks passed`를 확인한다.
2. `.cache/live-load-20261023`의 기존 토큰과 인증서를 확인한다. 토큰 권한은 `0600`이어야 한다.
3. 수정 전후의 `wss-relay`와 `relay-load`를 빌드한다. 실행 파일은 `out/shutdown-retest-20261024` 아래에 둔다.
4. `run-config.json`의 명령으로 전용 서버를 실행한다. 실행자는 직접 시작한 프로세스의 PID를 기록한다.
5. `record-bindings.py --tag baseline --server-pid PID`를 실행한다. 소스·실행 파일·CA 해시와 서버 명령을 확인한다.

수정 전후 실행 파일을 다른 하위 폴더에 두면 각 명령에 `--binary-dir`를 지정한다.
서버 명령과 공개 포트 `19443` 및 관리 포트 `19092`가 맞지 않으면 도구가 중단한다.
참가자 슬롯 200개·방 상한 512개와 기존 바이트 예산도 검사한다.
기존 결과 이름이 있으면 새 `--tag`를 지정한다. 도구는 기존 결과를 덮어쓰지 않는다.

## 수정 전

아래 명령의 `PID`를 실제 서버 PID로 바꾼다.
이 옵션은 실패 뒤에도 나머지 조건의 근거를 수집한다. 실패를 통과로 바꾸지 않는다.

```sh
python3 docs/evidence/v2/2026-10-24-shutdown-retest/run-load.py \
  --suite shutdown --tag baseline --server-pid PID \
  --continue-on-failure \
  --failure-note 'Baseline shutdown errors retained for comparison; every failed gate remains failed'
```

6개 결과의 `passed`와 종료 코드를 확인한다. 한 조건이라도 실패하면 전체 종료 코드는 1이다.
`--failure-note`에는 예상 실패의 근거를 적는다. 오류가 재현되지 않아도 실제 수치를 보존한다.

## 수정 후

1. 수정한 소스로 두 실행 파일을 다시 빌드한다. 빌드 로그의 종료 코드 0을 확인한다.
2. 수정 전 서버를 종료한 뒤 새 서버를 실행한다. 새 PID와 같은 포트를 확인한다.
3. `record-bindings.py --tag final-before --server-pid PID`를 실행한다. 수정한 소스와 실행 파일 해시를 기록한다.
4. 아래 명령을 실행한다. 6개 결과의 전체 수명주기 오류가 모두 0건인지 확인한다.

```sh
python3 docs/evidence/v2/2026-10-24-shutdown-retest/run-load.py \
  --suite shutdown --tag final --server-pid PID
python3 docs/evidence/v2/2026-10-24-shutdown-retest/run-sdk.py \
  --hz 20 --tag final --server-pid PID
python3 docs/evidence/v2/2026-10-24-shutdown-retest/run-sdk.py \
  --hz 10 --tag final --server-pid PID
python3 docs/evidence/v2/2026-10-24-shutdown-retest/record-bindings.py \
  --tag final-after --server-pid PID
```

소스·실행 파일·CA 해시가 측정 전후에 같은지 비교한다.
원인 진단용 소스가 있으면 `record-bindings.py --extra-source PATH`로 명시한다.
토큰 값과 개인 키는 결과에 넣지 않는다.

## 판정과 보존

Go 결과는 `baseline`, `before`, `after`, `cleanup`의 원본 상태를 보존한다.
`phaseMetricDeltas`는 준비·예열, 본 측정, 측정 뒤 구간을 나눈다.
SDK 결과는 원본 상태와 1초 간격 표본을 보존한다. SDK의 `errorDetails`는 오류가 난 단계와 시각을 남긴다.
`lifecycleMetricDelta`는 `rateRejected*`, `queueOverflows`, `writeErrors`의 전체 증가량이다.
쓰기 오류는 어느 구간에서도 제외하지 않는다. 지연·자원 기준과 회수 기준도 유지한다.

실행자는 결과 JSON·로그·구간별 오류·해시 비교를 검토한다.
검토 뒤 `REPORT.md`와 최종 회수 결과를 작성한다. 확인하지 못한 범위와 남은 실패도 적는다.
마지막으로 직접 시작한 서버를 종료하고 PID와 두 포트가 남지 않았는지 확인한다.

## 이번 실행의 판정

Go 6개 조건과 SDK 20Hz는 통과했다. SDK 10Hz는 재접속 구간 쓰기 오류 5건으로 엄격한 무오류 기준에 실패했다.
추가 SDK 진단 1건도 정상 Close 뒤 이전 연결의 알림 쓰기 오류로 기록했고, 실패 판정을 유지했다.
느린 수신자 20Hz 조건은 실제 쓰기 오류 13건을 검출했다. 이 결과는 성공한 부하 조건에 포함하지 않는다.
모든 원시 오류를 보존했다. 현재 서버 PID와 포트는 모두 회수했다.
