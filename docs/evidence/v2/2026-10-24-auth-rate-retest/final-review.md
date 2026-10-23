# Final independent review

Scope: uncommitted product/spec/test package in final-review.diff against dfcbdd35bba18fb981564fc6ba182ff6fe69172a; current source and six prepared retest harness/config files. Read plan, handoff, supporting HTTP admission/IP implementation, Go load producer, existing eight-frame rejection test, RED/GREEN logs, and final race/vet output. No tests, servers, loads, commits, or product edits performed by reviewer. This report is the only reviewer-written artifact.

## Strengths

- Authentication uses the existing normalized ClientIP result and existing mutex. Mapped IPv4 aliases share a counter. Untrusted XFF is ignored; trusted-proxy chains terminate at the first untrusted rightmost hop.
- Original global admission-before-IP-budget ordering is retained. Per-IP occupancy is reserved before upgrade and released on all subsequent returns; successful authentication releases before Welcome and the established read loop. IP entries disappear at zero. Existing global128, handshake pacing, total connection checks, and established lifetimes remain intact.
- Tests exercise 64 pending clients, another address authenticating, two addresses filling global128, mapped addresses, untrusted spoofing, credential failure, success, transport cancellation, upgrade failure, server cancellation, and reacquisition of all global slots.
- State change is only burst2 to burst6; sustained20/s, total40/s and byte limiters remain unchanged. Controlled room-lock pause test demonstrates genuine socket arrival compression, and the existing eight-immediate-frame test still verifies rejection. RED diagnostic output identifies message-kind rejection with available byte budget; GREEN output verifies both tests.
- SDK harness records phase snapshots and phase-tagged errors, checks actual public allocation, peer data, events, heartbeat, concentrated reconnect and cleanup. Python wrapper retains the whole-run write-error gate; deliberate disconnect errors cannot silently become success.
- Supplied code retains TLS verification through explicit CA trust and Go RootCAs. Token/private key contents are not explicitly serialized. Evidence records credential paths and public certificate hash, not secret contents. Source and binary hashes, build information and exact PID/path/port checks improve reproducibility.

## Issues

### Critical
None identified.

### Important
None identified in the product implementation.

### Minor
1. docs/evidence/v2/2026-10-24-auth-rate-retest/run-load.py:120-133: cleanup status is fetched but only occupancy/resource fields are retained. metricDelta covers nodeAfter-nodeBefore, which the Go producer samples after warmup and before cancellation. Consequently Go summaries cannot support an all-phase zero server-writeErrors/queueOverflows claim, and teardown error evidence is lost. Retain baseline-to-before and after-to-cleanup counter deltas, or label the existing metricDelta explicitly as measurement-only and limit final claims accordingly. This does not invalidate steady-state measurements or occupancy cleanup checks.
2. docs/evidence/v2/2026-10-24-auth-rate-retest/record-bindings.py:39: source inventory names nonexistent `proto` rather than `api/relay/v2`. Generated runtime Go/TS sources are hashed, so binary behavior remains identifiable, but the protocol source is absent from the claimed source inventory. Replace that path with `api/relay/v2` to bind the actual schema.

## Evidence interpretation

- Final go-race-final.log contains successful package results, including relay. go-vet-final.log is empty, consistent with success but not independently sufficient to prove its exit code; executor must retain/check command exit status.
- The natural baseline non-reproduction does not establish the exact cause of historical intermittent rejections. The controlled test proves a specific arrival-compression failure and its fix. Preserve this distinction in final reporting.
- Burst6 permits a finite full-bucket six-frame arrival batch; it does not guarantee tolerance of every repeated compression pattern or every load with <=250ms latency. Sustained refill and other budgets continue to apply, as the spec states.
- SDK phase deltas are sampled intervals. A write error in a reconnect/cleanup interval is correlated with that interval, not proof of a benign cause. The existing strict overall gate appropriately retains the failure.
- Config is declarative. Final live stats and process/build evidence must confirm the actual defaults, binary and limits. Hash capture itself is not a cryptographic proof that a separately launched process loaded those bytes; controlled rebuild/launch discipline supplies that binding.

## Declined to judge

- Final live200 load/reconnect/security success: executor has not yet completed sequential runs at review time. Harness readiness is reviewed; runtime outcome remains pending.
- Exact historical cause of intermittent rejection: not reproduced in the natural baseline; controlled evidence cannot retrospectively prove it.
- Distributed attack resistance across many source IPs, Linux/Internet/proxy/Android performance: outside the local same-host corrective scope and unavailable evidence.
- Existing voluntary-leave credential TTL behavior: explicitly deferred by handoff; unchanged here.
- External shell environment overrides of Node TLS verification: harness adds CA trust and never disables verification, but actual inherited environment/launch conditions require executor confirmation.

## Assessment

Spec verdict: product changes satisfy the bounded authentication isolation and finite state-arrival compression requirements. Final live retest requirement remains pending.

Quality verdict: ready for the planned sequential retest; no product blocker found. Two small evidence-quality improvements above should be addressed or explicitly scoped in reporting. No new abstraction or dependency is needed. Whole-task completion cannot be claimed until runtime results, process exit statuses and bindings are assessed.
