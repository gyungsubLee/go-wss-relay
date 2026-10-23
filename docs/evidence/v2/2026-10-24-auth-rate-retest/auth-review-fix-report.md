# Authentication admission review correction

Both review findings are addressed in the owned files `internal/relay/server.go` and `internal/relay/auth_admission_test.go`. No commit was created and no other worker's files were changed.

## Important: retain the global gate before IP processing

The initial correction moved ClientIP and IPBudget ahead of global admission. That changed the original protection boundary: requests rejected by the global rate or concurrency limit could still enter IPBudget, including its capacity cleanup scan of up to 16,384 entries.

Restored the original order: path validation, global `authAdmission.Enter`, ClientIP, IPBudget, then the existing mutex-protected connection and per-IP authentication checks. The cap remains 64 per effective IP and 128 globally. The shared release closure still returns both reservations on successful authentication and the deferred failure path, deleting zero-count map entries. No IP reservation exists before the mutex admission block, so earlier failures release only their global slot.

`TestAuthenticationAdmissionRejectsBeforeConsumingIPBudget` installs a one-slot global admission and a fixed-clock IP budget with rate zero and burst one. With the global slot held, a valid-path request returns 429. After releasing that slot, the same IP's next missing-upgrade request must reach WebSocket validation and return 426. This directly detects global rejections consuming the IP budget.

RED command, before the production correction:

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go test -count=1 -run '^TestAuthenticationAdmissionRejectsBeforeConsumingIPBudget$' -v ./internal/relay
```

Exit 1: `global rejection consumed IP budget: next upgrade returned 429, want 426`. Output: `auth-review-red.log`. This check uses request/response recorders and opens no sockets.

## Minor: synchronize lifecycle checks with authentication cleanup

The original test wait observed connection count only. Handler defer order can decrement `connections` before releasing the per-IP reservation, so a replacement dial could race that release.

The wait now observes both connection count and the sum of per-IP pending authentication counts under `server.mu`. It also requires the IP map to be empty whenever the expected authentication count is zero, including after `Server.Close`.

Global release follows the map update, leaving another narrow observation window. The final global-slot check now retries acquisition until all 128 slots are held or a two-second deadline expires, rather than assuming a map observation guarantees the subsequent global release has completed.

## GREEN and static checks

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go test -race -count=1 -run '^TestAuthenticationAdmission' -v ./internal/relay
```

Exit 0; all four authentication tests passed with the race detector. Output: `auth-review-green.log`.

```sh
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/go-mod" .tools/go/bin/go vet ./internal/relay
```

Exit 0; output: `auth-review-vet.log`. Both owned Go files were formatted with the bundled Go 1.26.10 `gofmt`. `git diff --check` exited 0. The complete Modern Go wrapper guidance was read again before the correction.

No source or test changed after GREEN. The root agent owns the final combined repository checks after both task corrections; this report makes no new whole-suite claim.

## Source bindings

```text
2cb32508c5c453d7393cab1eec279f95b7add3ac5a2fcc1797d898e77ac8532c  internal/relay/server.go
a68dd8736158dc8f30460bf70efeb34062cee34b9551bf96509171c8c4d74e21  internal/relay/auth_admission_test.go
```

## Log bindings

```text
85ec1cd3143bca1c8e14de7ed5e05b6f5a2d6f51d58da01bc718edd35dbed36f  auth-review-red.log
d382062b9402f69c06c6353d48994ea80befd8278d263d0909d0a5488b0ddba4  auth-review-green.log
```
