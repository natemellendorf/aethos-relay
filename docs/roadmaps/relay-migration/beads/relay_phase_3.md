---
id: relay_phase_3
title: "aethos-relay — Phase 3 support full Gossip V1 client encounter flow"
depends_on: [relay_phase_1, relay_phase_2]
external_dependencies: []
---

Bead: aethos-relay — Phase 3 support full Gossip V1 client encounter flow

Dependencies
- Depends on: aethos-relay — Phase 1 destructive rewrite to Gossip V1 stream/session foundation
- Depends on: aethos-relay — Phase 2 implement authenticated ingest boundary and durable RELAY_INGEST semantics


Problem


The relay must do more than exchange HELLO. It needs to participate in full client encounter flow using the new protocol:


HELLO → SUMMARY → REQUEST → TRANSFER → RECEIPT


This phase should complete relay-side client protocol behavior so upgraded clients can exchange objects with the relay using canonical Gossip V1 flow.


Goals


1. support SUMMARY handling in the relay
2. support deterministic REQUEST generation/handling where applicable
3. support TRANSFER validation/import and RECEIPT behavior
4. ensure relay-to-client object exchange is protocol-correct and idempotent
5. make the relay usable by upgraded clients without old protocol fallback


Challenge assumptions


- Do not hand-wave reconciliation behavior outside the protocol spec.
- Do not allow invalid objects to poison valid relay state.
- Do not preserve old client-facing protocol behavior.
- Do not start relay-to-relay transport work in this bead.


Scope


This phase should cover:
- relay/client SUMMARY handling
- relay/client REQUEST handling
- relay/client TRANSFER handling
- relay/client RECEIPT handling
- object import/export idempotency


This phase should not cover:
- relay-to-relay transport behavior
- propagation tuning
- scoring
- LAN/datagram support


Implementation tasks


1. Wire SUMMARY behavior
Ensure the relay can:
- emit SUMMARY from its eligible object set
- receive SUMMARY from a client
- use deterministic reconciliation behavior defined by the protocol


2. Wire REQUEST behavior
Ensure the relay can:
- receive REQUEST.want, including empty want as valid no-op
- generate/send REQUEST where required by the runtime path
- preserve deterministic ordering and validation rules


3. Wire TRANSFER behavior
Ensure the relay can:
- validate incoming TRANSFER objects independently
- accept valid objects and reject invalid ones according to protocol behavior
- keep mixed validity handling aligned with the chosen protocol rule
- export valid objects in outbound TRANSFER frames


4. Wire RECEIPT behavior
Ensure the relay can:
- generate RECEIPT for accepted objects where appropriate
- validate inbound RECEIPT against the immediately preceding valid TRANSFER in that direction


5. Add relay/client loopback or mock-peer tests
Add tests proving a full flow:
- HELLO
- SUMMARY
- REQUEST
- TRANSFER
- RECEIPT


6. Verify idempotency and duplicates
Ensure:
- repeated valid objects do not create duplicate durable state
- repeated encounters converge idempotently
- relay behavior remains stable across repeated flows


Acceptance criteria


- relay supports HELLO → SUMMARY → REQUEST → TRANSFER → RECEIPT with upgraded peers
- relay imports valid objects and rejects invalid ones correctly
- empty REQUEST.want is treated as valid no-op
- duplicate objects are handled idempotently
- no legacy client-facing protocol path remains reachable in the migrated flow


Validation plan


- run relay/client loopback or mock-peer tests
- verify full encounter flow succeeds
- verify duplicate/idempotent behavior
- verify mixed-validity transfer handling matches protocol expectations


Non-goals


- do not implement relay-to-relay transport behavior in this bead
- do not add datagram/LAN support
- do not add scoring/policy tuning


Outcome


At the end of Phase 3, the relay will be able to speak full Gossip V1 flow with upgraded clients over the stream/session path.
