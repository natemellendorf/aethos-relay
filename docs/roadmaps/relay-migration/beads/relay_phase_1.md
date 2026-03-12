---
id: relay_phase_1
title: "aethos-relay — Phase 1 destructive rewrite to Gossip V1 stream/session foundation"
depends_on: []
external_dependencies:
  - "Upgraded aethos core protocol implementation merged and usable by downstream repos."
---

Bead: aethos-relay — Phase 1 destructive rewrite to Gossip V1 stream/session foundation

Dependencies
- Requires the upgraded aethos core protocol implementation to be merged and usable by downstream repos.
- No dependency on Linux client rewrite.
- No dependency on iOS rewrite.


Problem


The relay must stop using legacy protocol/session assumptions and adopt the new Aethos Gossip V1 stream/session path.


We do not want backward compatibility or dual-path support. This is a destructive protocol rewrite for the relay. However, the first phase should stay narrow: prove the relay can host the new stream/session engine correctly before implementing durable ingest effects or full relay-to-relay behavior.


Goals


1. remove legacy protocol/session wiring from the relay
2. integrate Aethos Gossip V1 stream/session handling into the relay runtime
3. prove HELLO send/receive works over the relay’s existing stream-oriented path
4. align relay event/lifecycle handling with the new engine contract
5. leave durable ingest effects and full transfer behavior for later phases


Challenge assumptions


- Do not preserve old protocol code paths.
- Do not implement datagram/LAN behavior in this bead.
- Do not mix relay rewrite with relay policy tuning.
- Do not reimplement protocol logic if aethos core already provides it.


Scope


This phase should cover:
- destructive replacement of legacy protocol/session runtime wiring
- stream/session adapter integration
- HELLO validation and runtime state handling
- removal of legacy protocol assumptions from touched relay paths


This phase should not cover:
- durable ingest effects
- full SUMMARY/REQUEST/TRANSFER/RECEIPT flow
- relay-to-relay policy behavior
- observability expansion beyond minimal diagnostics


Implementation tasks


1. Audit relay protocol usage
Locate all places where the relay currently:
- parses or constructs old protocol frames
- assumes old session/gossip models
- uses old protocol-specific runtime glue
- derives trust from transport metadata in ways that conflict with the new spec
- bypasses core protocol behavior


2. Replace legacy protocol wiring
Update the relay runtime to use:
- Gossip V1 frame encoding/decoding
- stream/session adapter
- canonical CBOR framing and validation
- event callbacks from the new protocol engine


3. Wire the existing stream transport path
Target receive flow:
- bytes received from stream transport
- feed into Gossip V1 stream/session adapter
- receive validated protocol events
- dispatch into relay handlers


Target send flow:
- relay/runtime requests frame send
- canonical frame + u32be stream prefix encoded
- transport sends bytes


4. Add a relay-side loopback or mock-peer proof harness
Before relying on another upgraded peer, prove:
- relay sends HELLO
- peer receives and validates HELLO
- peer sends HELLO
- relay receives and validates HELLO
- session remains healthy


5. Align event handling with the new engine contract
Relay runtime must respect:
- state transition before terminal error
- fatal validation errors terminate encounter cleanly
- non-fatal observer failures remain non-fatal
- unauthenticated RELAY_INGEST is non-deliverable/non-effecting


6. Remove stale protocol assumptions from touched code
Delete or replace:
- old frame structs
- old session models
- old decoding helpers
- old runtime glue


Acceptance criteria


- relay no longer uses legacy protocol/session wiring in the migrated stream path
- relay can host Gossip V1 stream/session integration
- HELLO send/receive succeeds through loopback or mock-peer tests
- event ordering matches the new engine contract
- version mismatch fails closed
- no backward compatibility path is introduced


Validation plan


- run relay tests locally and in CI
- verify HELLO loopback/mock-peer exchange
- confirm fatal errors terminate cleanly without corrupting runtime state
- confirm old protocol path is no longer used in the migrated flow


Non-goals


- do not implement durable ingest effects in this bead
- do not implement relay-to-relay policy behavior
- do not implement datagram/LAN transport
- do not add backward compatibility


Outcome


At the end of Phase 1, the relay will be destructively switched onto the new Gossip V1 stream/session foundation, with loopback-proven HELLO exchange and legacy protocol runtime wiring removed from the migrated path.
