# Authentication admission Task 1

This records the initial implementation and its checks. The global-gate ordering review correction supersedes the final source bindings below; see `auth-review-fix-report.md` for the current change and evidence.

Task 1 is implemented in `internal/relay/server.go` and `internal/relay/auth_admission_test.go`. No commit was created. Other workers' files were not edited.

## Change

- Keep the global authentication concurrency limit at 128, the five-second first-frame timeout, and the existing 200/s per-IP handshake rate with burst 400.
- Resolve the existing trusted-proxy-aware, normalized `httpx.ClientIP` before consuming global admission.
- Track pending authentication by normalized IP under the existing `server.mu`; allow at most 64 pending attempts per IP.
- Return HTTP 429 with `Retry-After: 1` when that IP already owns 64 pending attempts.
- Return both the global slot and the IP reservation from the same release closure, on successful authentication or the existing deferred failure path. Delete an IP entry at zero. The `ipReserved` flag prevents a pre-reservation rejection from decrementing another handler's reservation.
- Keep established connections outside the pending authentication count. Leave `httpx.Admission`, `IPBudget`, the control API, protocol, SDK, and byte budgets unchanged.

## Regression coverage

`TestAuthenticationAdmissionPreservesOtherIPsAndGlobalCapacity` opens 64 silent connections using both IPv4 and mapped IPv4 forms, rejects the next attempt from that IP, authenticates a different effective IP, fills the remaining global capacity, verifies the global 128 limit, then verifies admission recovers after silent peers close.

`TestAuthenticationAdmissionIgnoresUntrustedForwardedAddresses` verifies changing an untrusted XFF header cannot obtain a different budget.

`TestAuthenticationAdmissionReleasesEveryCompletedAttempt` checks release after successful authentication while the authenticated connection stays open, reused-credential rejection, peer cancellation, rejected HTTP upgrade, and server cancellation. After server cancellation it reacquires all 128 global admission slots. Text frames, malformed first frames, and timeout expiry use the same deferred release path but were not individually added as socket cases.

The existing `internal/httpx/ip_test.go` remains responsible for exact trusted-chain selection and malformed-chain rejection. The new socket checks exercise trusted and untrusted headers through `ServeHTTP`.

## Execution evidence

All commands used Go 1.26.10 with `GOCACHE="$PWD/.cache/go-build"` and `GOMODCACHE="$PWD/.cache/go-mod"`.

1. Modern Go wrapper `list --file-path internal/relay/server.go`: exit 0; complete guidance read before editing. Applied `t.Context()` and integer ranges in new tests.
2. `.tools/go/bin/go test -run '^$' ./internal/relay`: exit 0, no tests run; confirms new test file compiles before the fix.
3. Initial sandbox RED run could not bind loopback ports. This is an environment failure, not regression evidence. Preserved in `auth-red-sandbox.log`. Escalated local test execution was then approved automatically.
4. RED against unchanged production `server.go`: `.tools/go/bin/go test -count=1 -run '^TestAuthenticationAdmission' -v ./internal/relay`: exit 1. The first two tests received HTTP 101 for the 65th attempt where HTTP 429 was required. The lifecycle test passed. Full output: `auth-red.log`.
5. GREEN after the fix: `.tools/go/bin/go test -race -count=1 -run '^TestAuthenticationAdmission' -v ./internal/relay`: exit 0; all three tests passed. Full output: `auth-green.log`.
6. Required whole-repository run: `.tools/go/bin/go test -race -count=1 ./...`: exit 1. All packages except the relay package and a diagnostic evidence directory passed. Relay failed the other task's intentionally RED `TestPacedStateUpdatesSurviveReaderPause`, with rate-limited sequence 4. `docs/evidence/v2/2026-10-24-auth-rate-retest/diagnostic-connection.go` also failed standalone compilation because Server, room, member, and other relay declarations were unavailable in that evidence package. Both failures were reported to the root agent; they are outside Task 1 ownership. Full output: `auth-full-suite.log`.
7. `.tools/go/bin/go vet ./internal/relay`: exit 0; output: `auth-vet.log`.
8. `gofmt` applied to both owned Go files; `git diff --check`: exit 0.

After the GREEN and full-suite runs, one explanatory comment was added above `maxAuthenticatingPerIP`; no executable code or test changed afterward. The hashes below identify the final files including that comment. The root agent will perform the final combined suite after Task 2 is implemented and the diagnostic source discovery issue is resolved.

## Final source SHA-256

```text
5d4b23a90ca40ed2f201d55e8a78e3f16753c0c3c7dacc07f2627c27374d86f8  internal/relay/server.go
646e2e251756a8cb7391cb9469a5665700964460297911d68f5536722dc548b5  internal/relay/auth_admission_test.go
```

## Log SHA-256

```text
48779da4bb87134b3b727d11639b6bc4f24b05ad8d62020a0a4b5e6504976ce4  auth-red.log
41727d6fed9e6a62b219a25944e001cb007779a42049b59eac1225c7f03af74e  auth-green.log
c44b8a63d408888d404d2412d7db9dacf853bbc64c6406d30c024a2bf555e209  auth-full-suite.log
```

## Remaining acceptance work

The same-IP 200-client SDK setup and concentrated reconnect remain for the root's sequential load retest. The cap applies to pre-authentication attempts, so established sessions do not consume it. Existing SDK handshake-failure retry with jitter should handle reconnect bursts, but this report does not claim that load test has passed.
