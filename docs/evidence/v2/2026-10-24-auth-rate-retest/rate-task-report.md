# 상태 전송 제한 수정 결과

상태의 순간 허용량을 2개에서 6개로 바꿨다. 통제한 수신 정지 검사는 실패 후 통과했고, 기존 연속 전송 차단 검사도 통과했다.
전체 race 검사와 `go vet`의 종료 코드는 0이다. 이 결과는 인증 순서의 검토 수정을 적용하기 전 소스에만 연결한다.

## 확인한 원인과 범위

기존 제한기는 초당 20회 상태를 보내도 서버가 밀린 프레임을 함께 읽으면 거부했다.
[`rate-red.log`](rate-red.log)에서 첫 읽기 뒤 `259.085ms` 간격으로 다음 상태를 읽었다.
그 뒤 sequence 4·5·6은 `message_kind` 제한으로 거부했다. 직전 읽기 간격은 각각 `3µs`·`354µs`·`13µs`였다.
전체 메시지 허용량은 36개 이상 남았다. 노드 수신 예산은 2MiB, 방 수신 예산은 131,035바이트 이상 남아 있었다.
이 검사는 상태 제한기가 정상 간격의 송신을 거부하는 경로를 확인했다.

이번 자연 부하에서는 이전 오류를 직접 재현하지 못했다.
실제 SDK 200명·20Hz·679바이트·30초는 오류 0건이었다. Go 200명·10Hz·679바이트·60초도 제한 오류 0건이다.
따라서 과거 자연 부하의 오류도 같은 원인이었다고 확정하지 않는다.
근거는 [`sdk-baseline.json`](sdk-baseline.json)과 [`go-baseline-10hz60.json`](go-baseline-10hz60.json)에 있다.

## 변경한 동작

- `internal/relay/connection.go`: 상태 제한만 `rate.NewLimiter(20, 6)`으로 바꿨다. 초당 20회 속도는 유지한다.
- `internal/relay/rate_jitter_test.go`: 방 잠금을 유지한 채 상태 6개를 50ms 간격으로 보낸다. 잠금을 푼 뒤 오류 없이 마지막 상태를 전달하는지 확인한다.
- `docs/specs/2026-10-20-relay-v2-message-spec.md`: 순간 허용량 6개와 250ms 한계를 적었다. 인증 대기는 IP별 64개, 노드 전체 128개로 명시했다.

50ms 간격 상태는 양 끝을 포함한 250ms 구간에 최대 6개다. 이 수치는 250ms 동안 생긴 수신 편차를 받는 유한 예산이다.
첫 상태의 제한 차감이 이미 끝났다면 밀린 5개에는 순간 허용량 5개로도 충분하다.
6개는 첫 상태까지 함께 제한을 검사하는 경계 시점을 포함한다. 250ms를 넘는 수신 정지까지 보장하지는 않는다.
전체 메시지 제한 40회/s와 모든 바이트 예산, 사건·Ping·Resync 제한은 바꾸지 않았다.
새 의존성이나 대기 큐도 추가하지 않았다.

## 실행한 검사

저장소 루트에서 다음 명령을 실행했다. 세 명령 모두 종료 코드 0을 반환했다.

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go test -count=1 -v ./internal/relay -run '^(TestPacedStateUpdatesSurviveReaderPause|TestPublisherRateDoesNotBlockNewRoomsAndMetricsCountTraffic)$'
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go test -race -count=1 ./...
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go vet ./...
```

- [`rate-green.log`](rate-green.log): 수신 정지 검사와 기존 8개 연속 상태의 차단 검사가 통과했다.
- [`rate-full-race.log`](rate-full-race.log): 전체 패키지가 통과했다. race 오류는 없었다.
- [`rate-vet.log`](rate-vet.log): 출력이 없었으며 종료 코드는 0이었다.

`gofmt`와 `git diff --check`도 오류 없이 끝났다. 제품 코드를 바꾸기 전 RED는 상위 에이전트가 실행한 로그를 읽고 확인했다.
이번 검사에서는 진단 overlay를 적용하지 않았다. 상태 제한은 제품 소스로 검사했다.

## 소스 연결

Go 실행 버전은 `go1.26.10 darwin/arm64`다.
[`rate-task-source-sha256.json`](rate-task-source-sha256.json)에 50개 파일과 네 로그의 SHA-256을 기록했다.

| 파일 | SHA-256 |
|---|---|
| `internal/relay/connection.go` | `f8a7442946891787f063c52f3d52caf7487036f961d7eb31ccd58f5bcbdcd6cb` |
| `internal/relay/rate_jitter_test.go` | `3f6c15c4848d5e4ae25868bca4451e3c336f3c7f1b5014591ee7bb0a377db3d6` |
| `internal/relay/server.go` | `5d4b23a90ca40ed2f201d55e8a78e3f16753c0c3c7dacc07f2627c27374d86f8` |
| `docs/specs/2026-10-20-relay-v2-message-spec.md` | `a1fba2c31e1f6fc02a38ac8c68547162ecad9ee4f597400c626191b4a248df75` |

## 남은 검사

인증 담당자가 `server.go`의 전역 인증 제한 순서를 고친 뒤 상위 에이전트가 전체 검사를 다시 실행한다.
같은 조건의 최종 부하도 상위 에이전트가 실행한다. 이번 작업은 서버를 시작하거나 자연 부하를 다시 실행하지 않았다.
소켓 검사 시간은 이미 반환했다. 커밋은 만들지 않았다.
