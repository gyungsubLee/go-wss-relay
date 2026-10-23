**I1 — Overlapping entry actions discard the first successful participation** — **ADDRESSED**, `client/src/demo/main.ts:154`. The shared guard rejects all three entry siblings before epoch invalidation, claims pending ownership and disables all entry controls synchronously (`:155–158`). Create/join/match all pass through that boundary (`:172`, `:178`, `:211`). The first success keeps its epoch and retains lobby/ticket polling and leave/cancel availability. Cleanup restores entries only for the same live epoch without membership (`:163–165`); ordinary failures and invalid join permit retry, while stale/disposed completion cannot revive controls. Both `enterLobby` callers were checked before accepting removal of its redundant entry-button disabling.

### New Breakage in the Fix Diff

None — no Critical, Important or Minor finding in the two-file fix from `790fec4c59c18d13bc63549d815b0c74932affb9` to `d182e70e1fc1ec94d6a1417b53ad99febd32dc15`.

### Checks and Evidence

- Read the complete supplied fix diff, original I1/acceptance, fix brief, appended report, bindings and affected entry/exit/polling/pagehide flow. Review scope remained I1 and new breakage introduced by this fix; no second whole-branch review was performed.
- `client/scripts/check-demo-entry.mjs:17` extracts and transpiles production state/handlers/lifecycle blocks; deferred API, DOM and timer boundaries do not replace the production guard. Its nine pending sibling combinations (`:100`) check one mutation, unchanged owner epoch, retained membership, controls and polling, including rejection after success. Remaining cases cover each entry failure/retry, invalid join, late success/failure after disposal, stale entry cleanup, successful leave/cancel followed by stale query success/failure and re-entry, retryable exit failure, and pagehide queries.
- Read actual retained RED output/status in `final-fix-red.log`: assertion `create disabled=true`, `false !== true`, exit **1**. The report binds it to original main hash `fbd837376b95fecd26060afa7062d4a6714cc29c3c258c5288969df425be098c` before the production edit. Read the unsuccessful GREEN fixture correction separately; it failed at re-entry because cleared join input was not replenished, and is not presented as product RED.
- Read every final GREEN line in `final-fix-green.log`: **31 scenarios**, final `PASS demo entry and affected lifecycle regression checks`, exit **0**. Read `final-fix-build.log`: `tsc --noEmit && vite build`, 94 transformed modules, exit **0**. No suites were rerun for this review; existing runs answer the affected-code questions.
- Independently recalculated all **9 current file hashes** and **4 retained log hashes** against `final-fix-bindings.json`, checked each embedded exit status and counted the 31 GREEN scenarios. Read-only Python verification exited **0**, ending `PASS final sources 9, retained logs 4, GREEN scenarios 31; exit_status=0`. Main also matches `final-entry-browser-source.json` hash `d04b51b7c7a6f5685ef81ed0cbd410e306d8f422243a165b4bf825840894d644`.
- Read controller Chrome evidence and assertion log (`final-entry-browser.md`, `final-entry-browser-check.log`, exit **0**) plus retained JSON/AX. Double-create has one real POST201 and subsequent GET200 polling with leave enabled; double-join has one POST200 and 2/2 participants. Failure/retry traces show create503→201 and queue503→one201, with restored entries and retained ticket polling/cancel. Fresh owner/peer AX shows both connected and event delivery. These are controller-operated actual Chrome observations; this reviewer did not launch a browser or server.

### Out-of-Scope Observations

None newly found. The original successful-query-after-exit Chrome DOM limitation remains recorded; entry-mutation Chrome evidence does not erase it. The maintained production-helper regression separately covers stale query success/failure. Task 10 status/evidence closure and historical main-hash annotations remain the controller's expected post-gate work, not a new finding. Earlier Go/SDK/protocol/operations and host/device/CI/load limits are unchanged.

### Verdict

**Fix round: All findings addressed, no new Critical/Important breakage.** I1 is closed. This report does not authorize push, merge or deployment.
