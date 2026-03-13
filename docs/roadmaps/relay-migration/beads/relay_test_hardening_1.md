---
id: relay_test_hardening_1
title: "Bead: aethos-relay — Test hardening 1 eliminate flaky sleeps"
depends_on:
  - relay_phase_4
external_dependencies: []
---

Bead: aethos-relay — Test hardening 1 eliminate flaky sleeps

Dependencies
- Depends on: aethos-relay — Phase 4 align relay-to-relay Gossip V1 behavior and add interoperability coverage

Problem

Some Gossip V1 interoperability tests rely on fixed wall-clock sleeps to assert negative conditions (e.g. “no summary received”). These sleeps can be flaky under CI contention and reduce confidence in the migration phases.

Goals

1. remove fixed `time.Sleep` synchronization from Gossip V1 tests where feasible
2. replace fixed sleeps with bounded polling or event-driven waits
3. keep all changes test-only (no relay runtime/protocol behavior changes)
4. improve determinism and reduce CI flake probability

Challenge assumptions

- Do not change production relay behavior in this bead.
- Do not change protocol semantics.
- Prefer small, mechanical refactors over broad test rewrites.

Scope

This bead should cover:
- auditing tests for fixed sleeps and timing-based assertions
- replacing sleeps with bounded polling helpers (with timeouts)
- tightening negative assertions to be robust under variable scheduling

This bead should not cover:
- performance tuning
- protocol changes
- adding new major test harnesses

Implementation tasks

1. Audit timing-based tests
Locate all uses of `time.Sleep` and other fixed timing assumptions in tests.

2. Introduce a small bounded wait helper
Add a small test helper (package-local or shared testutil) that polls for a condition with a timeout and small interval.

3. Replace fixed sleeps
Refactor identified tests to use the bounded wait helper.

4. Stress-run the affected tests
Run the relevant tests repeatedly (e.g. `-count=25`) to reduce the chance of hidden flakiness.

Acceptance criteria

- no fixed `time.Sleep` remains in Gossip V1 interop tests for negative/absence assertions
- tests remain readable and deterministic
- `go test ./...` passes

Validation plan

- run `go test ./...`
- run focused repeated runs for the updated packages/tests

Outcome

At the end of this bead, Gossip V1 interoperability tests will avoid fixed sleeps and be less flaky under CI scheduling variability.
