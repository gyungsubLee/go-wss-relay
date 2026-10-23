# Final whole-branch review

Base: `ba3db51`. Reviewed HEAD: `790fec4c59c18d13bc63549d815b0c74932affb9` (`codex/wss-v2`).
Authority: `docs/superpowers/specs/2026-10-23-go-structure-refactor-design.md` and `docs/superpowers/plans/2026-10-23-go-structure-refactor.md`.
Verdict: **With fixes — 0 Critical, 1 Important, 0 Minor.**

This is one bounded review of the complete branch, including cross-task behavior, rather than an aggregation of the ten task verdicts. The inventory, active-code packet, current-docs packet, current surrounding implementation, selected base files, original reports and retained evidence were inspected. Deleted v1 source/tests/generated assets and 24 obsolete documents were assessed against the explicit deletion matrix; active shared functionality was traced into its surviving consumers. No source, index, HEAD or branch changes were made. The only written artifacts are this report and the controller-requested read-only reproduction script.

## Strengths

- The dependency direction is coherent: the small command invokes `app.Run`, app composes concrete modules, and coordinator implements the existing control allocator. Manager owns state and detaches mutable views before HTTP encoding. No repository abstraction, actor layer, runtime dependency or compatibility wrapper was added.
- The allocation changes address the shared contract. Lobby and FIFO use the same prepare/remote-call/commit flow; formed tickets share a deadline; a delayed result cannot revive an explicitly ended group. Bounded structured node refusals are distinguished from ambiguous 503s, and the same reservation owner is retained through unhealthy periods. Negative histogram overflow now excludes a node from new placement.
- Token extraction preserves the active player-token derivation and frozen fixture. The runtime and coordinator both use the bounded private-file reader. Relay file splitting retains server → room → client lock order, a single socket writer, generation replacement, bounded reliable queues and state coalescing.
- Runtime ownership is visible from construction through cleanup. Failed listener setup, cancellation, draining and node idle connections have focused checks. SDK refresh success/failure/finally use the same session epoch, and the HTTP timeout includes body consumption.
- Protocol source and generated Go/TS bytes remain unchanged from the base. I independently decoded the old aggregate baseline and verified that its `relay.v2` descriptor exactly equals the new v2-only baseline. The staged dual-generator gate checks both languages and retains the baseline through expected failure paths. `go.mod` and `go.sum` are byte-identical to base.
- Operations retain archive validation, complete checksum coverage, locking, atomic current switching, rollback and durable fail-closed accounting. Live configuration is not installed from examples. The narrower deployment format is documented and the three prior preflight findings are corrected at the common input boundaries.
- Evidence is unusually explicit about what ran. It distinguishes real HTTPS/WSS processes, local Chrome HTTP/WS, mocked host commands, Linux ELF inspection and unexecuted host/device conditions. Historical measurements remain dated; pending product work is not silently marked complete.

## Issues

### Critical

None found.

### Important

#### I1 — Entry actions discard a successful server mutation when another entry action starts

- **Location:** `client/src/demo/main.ts:153` (create), `:160` (join), `:194` (quick match), with epoch invalidation at `:231`. Server ownership is enforced at `internal/control/manager.go:210` and `internal/control/queue.go:21`.
- **Trigger:** Start create and click create again before the first response arrives. Both controls remain actionable. Both handlers increment `participationEpoch`; the first request commits a lobby, and the second receives 409 because that participant already belongs to the lobby. When the first 201 resolves, its epoch is stale, so its lobby is discarded. The same root cause spans create→join/match, join→create/match or another lobby, and quick-match→create/join. Quick match disables only its own button before awaiting the response, so the cross-action paths remain open.
- **Observed impact:** `lobbyId` and `ticketId` remain empty; no polling starts and the leave/cancel control is unavailable. Subsequent entry attempts conflict with the server membership. A newly created lobby can hold that identity for its 30-minute lifetime; an unformed queue ticket lasts two minutes. Reloading for a new guest identity abandons the old membership rather than recovering it. This is a visible regression from base: the old handler accepted the first successful result instead of invalidating it on a second entry click.
- **Evidence:** The read-only exact-source handler probe confirmed five allowed interleavings. The controller independently reproduced the double-create case in actual connected Chrome on unchanged HEAD: tab `1490580648`, URL `http://127.0.0.1:38173/`, first real create response delayed three seconds (within the five-second deadline), two real POST outcomes 409 then 201, and no subsequent lobby GET. The retained accessibility output still shows the initial room screen and disabled leave/ready/start controls. See `final-entry-red.json`, `final-entry-red.ax.txt`, `final-entry-red.jpg` in this directory. I read the trace and accessibility evidence; the controller performed the browser interaction.
- **Minimum correction:** Serialize all participation-entry mutations with one shared in-flight guard and synchronously disable create, join and quick match before the first await. A second action must not invalidate the owner of a pending successful mutation. Restore the entry controls on an applicable failed entry; retain the existing exit/disposal guards and current membership on successful entry. Reuse the current explicit lifecycle functions; no polling framework or new server API is needed for this race.
- **Acceptance:** A double-create and each permitted cross-action sibling must issue at most one participation mutation, retain the returned lobby/ticket, start the appropriate polling, and make leave/cancel available when allowed. An ordinary entry failure must permit another attempt. Existing successful leave/cancel/pagehide stale-query checks must still pass. Add one runnable regression for this shared entry boundary and obtain the already available focused Chrome confirmation.

### Minor

None found. Existing pending-review text is expected to be closed only after this finding is resolved; it is not a separate documentation defect.

## Verification and review coverage

The final source bindings were independently recalculated, rather than inferred from passing task verdicts:

```text
final-go-source-hashes.json matched 47 exceptions [] mismatches []
final-process-source-hashes.json matched 62 exceptions [] mismatches []
task-8-source-hashes.json matched 129 exceptions [] mismatches []
task-9-final-bindings.json matched 70 exceptions [] mismatches []
task-7-source.sha256 matched 9 exceptions ['client/package.json', 'out/wss-relay'] mismatches []
task-5-browser-backend-Go matched 46 exceptions [] mismatches []
original evidence matched 52 mismatches []
```

The two Chrome exceptions are explicitly explained in durable evidence: npm commands changed afterward, and rebuilding changed binary metadata while the Go source stayed the same. They were not counted as matching original binary/package hashes.

Read original status/output for Go race/vet/fmt/govulncheck/package graph; protocol generation and drift/failure regressions; TS build; lifecycle with real 200/503 stalled bodies; wire 17; actual SDK sockets; cluster six and Snake flows; operations 79 transactions, guard/token checks and ELF/checksum inspection. Current evidence accurately includes valid Go test cache use. Unchanged suites and packaging were not rerun for this review.

Additional independent read-only descriptor/source comparison completed with exit 0:

```text
UNCHANGED api/relay/v2/relay.proto
UNCHANGED gen/go/relay/v2/relay.pb.go
UNCHANGED client/src/relay/gen/relay/v2/relay_pb.ts
UNCHANGED go.mod
UNCHANGED go.sum
PASS: current v2 descriptor exactly equals base aggregate v2 descriptor; exit_status=0
```

The first attempt to load the descriptor helper used a nonexistent internal npm path and exited 1 (`MODULE_NOT_FOUND`). It made no changes and did not reach a product assertion. Retrying through the package's public exports produced the exit-0 comparison above; this is a reviewer harness correction, not product RED evidence.

The architectural pass covered app flags/listeners and disposal; HTTP authentication/CORS/input/error contracts; Manager mutable views and membership; local/distributed allocator refusals and deadlines; node TLS/configuration, recovery and incarnation fencing; relay reservation/credentials/connection writers; SDK refresh/body timeout; demo polling and entry/exit interleavings; generation/CI ordering; deployment preflight/rollback/accounting; deletion classification and current requirement/status/evidence documents. The new entry-action finding is the only actionable issue from that pass.

### Focused I1 probe

Run from the repository root:

```sh
sh .superpowers/sdd/2026-10-23-go-structure-refactor/final-entry-probe.sh
```

The script preserves the probe executed in tool chunk `22b9c4`; it extracts and transpiles the actual current handlers and lifecycle helpers, with deferred API outcomes and minimal UI boundaries. It does not simulate the whole browser or backend, and is explicitly a **bug-confirming** check: exit 0 means the bad state was reproduced. The separate Chrome trace uses the real server. Its raw original stdout was:

```text
{"case":"create then create","secondEnabled":true,"serverMutation":"first request commits membership; second returns409","entered":[],"state":{"lobbyId":"","ticketId":"","participationEpoch":2},"logs":["요청 실패"]}
{"case":"create then join","secondEnabled":true,"serverMutation":"first request commits membership; second returns409","entered":[],"state":{"lobbyId":"","ticketId":"","participationEpoch":2},"logs":["요청 실패"]}
{"case":"join then create","secondEnabled":true,"serverMutation":"first request commits membership; second returns409","entered":[],"state":{"lobbyId":"","ticketId":"","participationEpoch":2},"logs":["요청 실패"]}
{"case":"match then create","secondEnabled":true,"serverMutation":"first request commits membership; second returns409","entered":[],"state":{"lobbyId":"","ticketId":"","participationEpoch":2},"logs":["요청 실패"]}
{"case":"match then join","secondEnabled":true,"serverMutation":"first request commits membership; second returns409","entered":[],"state":{"lobbyId":"","ticketId":"","participationEpoch":2},"logs":["요청 실패"]}
CONFIRMED: five permitted mutation interleavings orphan the first successful participation in exact source handlers; exit_status=0
```

Tool process exit code: **0**. The generic log string results from the minimal VM's cross-realm Error object; the assertions concern membership IDs, entered state and enabled controls, not message formatting.

## Declined to judge

This is the exhaustive list of behaviors considered but set aside in this bounded review. These are explicit limits for the controller to rule on, not findings silently discarded.

- Continued v1 HTTP/UDP/carrier/C#/Unity operation and its old regression suite — removal is expressly approved; retained shared v2 ID/token/HTTP consumers were reviewed instead.
- Restoring removed v1 plans/ADRs/evidence as live compatibility guidance — the approved deletion matrix stores that history in Git; active v2 contracts and historical v2 measurements remain.
- Adding persistence for lobbies/tickets, multi-writer coordinators, live room migration, game-authoritative validation, Redis/DB/Kubernetes or new abstraction layers — expressly excluded from this refactor, and none is required to fix I1.
- Generic recovery of a create/queue mutation whose successful response is permanently lost on the network — the existing API has no mutation identifier/recovery lookup for that broader product feature. I1 is narrower: this patch itself discards an actually received successful response during an avoidable local overlap and must be fixed.
- Real Linux process execution, systemd hardening/ordering, Caddy startup, public DNS/TLS/firewall and private-node certificate connectivity — local host stubs and ELF inspection cannot prove these; HOST-01 remains open and no live-host action is authorized.
- Fresh Linux/Darwin tool downloads and remote GitHub CI execution — checksum/platform definitions and bound local gate results were reviewed, but these external execution environments were not run; CI-01 remains open.
- Actual Android/WebView origin, device lock/background/termination, Wi-Fi/mobile-radio transition, packet loss and slow-device behavior — device evidence is absent, and the original ANDROID/NETWORK requirements stay open.
- New whole-load benchmarks and target-host capacity/SLO claims — this refactor did not rerun full load; dated 2026-10-22 measurements and message-size checks cannot establish new production capacity.
- Actual Chrome DOM consumption of a delayed successful **query** after leave/cancel — prior holds expired before the click. Existing guards were reviewed, but the SDK deferred-refresh checks do not prove this distinct browser behavior. The new successful **entry mutation** reproduction establishes I1 only and does not erase the query-specific limit.
- A new threat model for a malicious operator, hostile trusted relay configuration, or a fully compromised root-owned deployment bundle — existing trust boundaries are preserved; a content checksum is not publisher authentication, and the operations document says so. Untrusted public HTTP/WSS input and credential/file boundaries were included in the review.
- Full application redesign, all historical product gaps, or a guarantee that every unchanged line is defect-free — this is a whole-branch architectural/integration review against the approved refactor, not certification of the unfinished original product.

## Recommendations

Resolve I1 once at the shared entry boundary, run its focused regression and Chrome check, then perform the scoped fix review. Refresh affected frontend evidence bindings and final status metadata after that result. Keep the recorded device/host/CI/load limits intact. No additional broad review loop or rerun of unchanged Go/operations/protocol suites is justified by this finding.

## Assessment

**Ready to merge? With fixes.** The v2-only structure and preserved wire/auth/ownership contracts have strong code and bound execution evidence. The participation epoch change introduces one reproducible user-visible loss of successful membership, so the final refactor gate should remain open until I1 is corrected; this verdict does not authorize push, merge or deployment.
