---
id: relay_phase_4
title: "aethos-relay — Phase 4 align relay-to-relay Gossip V1 behavior and add interoperability coverage"
depends_on: [relay_phase_3]
---

Bead: aethos-relay — Phase 4 align relay-to-relay Gossip V1 behavior and add interoperability coverage

Dependencies
- Depends on: aethos-relay — Phase 3 support full Gossip V1 client encounter flow


Problem


Once the relay can speak full Gossip V1 to clients, the next step is to ensure relay-to-relay behavior is also aligned with the new protocol.


This phase should focus on relay-to-relay interoperability, idempotent replication behavior, and protocol-correct exchange between relays without drifting into broad propagation-policy tuning.


Goals


1. align relay-to-relay behavior with Gossip V1
2. prove relay-to-relay interoperability through tests
3. ensure relay replication remains idempotent and protocol-correct
4. prepare the relay layer for later policy/propagation tuning work


Challenge assumptions


- Do not implement scoring or advanced propagation policy in this bead.
- Do not leave relay-to-relay behavior as an implicit special case.
- Do not bypass the same framing and validation rules used elsewhere.
- Do not mix broad observability work into this phase.


Scope


This phase should cover:
- relay-to-relay stream/session behavior
- relay-to-relay SUMMARY/REQUEST/TRANSFER/RECEIPT alignment
- interop and idempotent replication tests
- edge-case handling for repeated relay encounters


This phase should not cover:
- scoring
- propagation-horizon tuning
- client UI/runtime work
- datagram/LAN transport


Implementation tasks


1. Audit relay-to-relay path
Locate any remaining old assumptions or relay-specific shortcuts in relay-to-relay exchange logic.


2. Align relay-to-relay encounter flow
Ensure relays use the same Gossip V1 rules for:
- framing
- validation
- reconciliation
- transfer/import behavior
- receipt behavior


3. Add relay-to-relay interop tests
Add tests proving:
- two upgraded relays can complete HELLO → SUMMARY → REQUEST → TRANSFER → RECEIPT
- duplicate object exchange is idempotent
- repeated relay encounters converge cleanly
- invalid objects do not corrupt valid relay state


4. Add cross-role coverage where helpful
If practical, add tests or fixtures proving:
- relay-generated frames are accepted by client-like peers
- client-generated frames are accepted by relay
- relay-generated frames are accepted by another relay


Acceptance criteria


- relay-to-relay behavior is aligned with Gossip V1
- relay-to-relay full encounter flow is tested
- repeated relay encounters converge idempotently
- no legacy relay-to-relay protocol path remains reachable in the migrated flow


Validation plan


- run relay interop tests locally and in CI
- verify full relay-to-relay flow
- verify duplicates and repeated encounters remain idempotent
- verify invalid objects do not poison valid relay state


Non-goals


- do not implement scoring or propagation tuning
- do not add datagram/LAN transport
- do not expand into broad observability work


Outcome


At the end of Phase 4, the relay will be fully aligned to Gossip V1 for both client-facing and relay-to-relay behavior, with interoperability coverage in place and ready for later policy tuning.
