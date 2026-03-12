---
id: relay_phase_2
title: "aethos-relay — Phase 2 implement authenticated ingest boundary and durable RELAY_INGEST semantics"
depends_on: [relay_phase_1]
external_dependencies: []
---

Bead: aethos-relay — Phase 2 implement authenticated ingest boundary and durable RELAY_INGEST semantics

Dependencies
- Depends on: aethos-relay — Phase 1 destructive rewrite to Gossip V1 stream/session foundation


Problem


After the relay can host the new Gossip V1 session foundation, it must correctly implement relay-specific ingest behavior.


The relay is the authoritative durable-storage participant in the system. It must only emit RELAY_INGEST after durable write and must preserve the trust boundary defined by the protocol.


This phase should implement the relay-side durable ingest boundary and authenticated RELAY_INGEST behavior without yet expanding into full relay-to-relay replication policy.


Goals


1. implement relay durable-write boundary for ingested objects
2. emit RELAY_INGEST only after durable persistence succeeds
3. preserve authenticated trust semantics for RELAY_INGEST
4. keep ingest behavior idempotent by item_id
5. avoid introducing client- or policy-specific pruning logic here


Challenge assumptions


- Do not emit RELAY_INGEST before durable write completes.
- Do not blur frame validity with trusted effect.
- Do not implement downstream client pruning/de-escalation policy in the relay.
- Do not conflate durable ingest with relay-to-relay propagation policy.


Scope


This phase should cover:
- durable ingest path
- RELAY_INGEST emission semantics
- authenticated relay trust boundary
- idempotent relay-side item handling


This phase should not cover:
- full relay-to-relay gossip scheduling
- scoring or propagation tuning
- broad observability work


Implementation tasks


1. Identify relay ingest path
Map where validated transferred objects become relay-stored objects.


2. Enforce durable-write-before-ingest semantics
Ensure RELAY_INGEST is emitted only after:
- object validation succeeds
- object persistence succeeds
- item_id-based dedupe is applied


3. Preserve authentication boundary
Ensure RELAY_INGEST is only trusted/effective on authenticated relay transport contexts.


4. Keep ingest idempotent
Repeated ingest of the same item_id must not create duplicate durable state or duplicate logical relay-ingest effects.


5. Add focused tests
Add relay-side tests proving:
- durable write precedes RELAY_INGEST
- duplicate ingest is idempotent
- RELAY_INGEST is not emitted on failed persistence
- unauthenticated RELAY_INGEST has no trusted effect
- authenticated RELAY_INGEST path remains observable and correct


Acceptance criteria


- relay emits RELAY_INGEST only after durable write
- relay ingest is idempotent by item_id
- authenticated boundary behavior matches protocol expectations
- tests cover success, duplicate, and failure paths


Validation plan


- run relay tests locally and in CI
- verify durable-write-before-ingest behavior
- verify no duplicate durable state for repeated ingest
- verify trust boundary behavior


Non-goals


- do not implement client pruning/de-escalation logic here
- do not implement relay-to-relay scheduling or policy tuning
- do not add datagram/LAN transport


Outcome


At the end of Phase 2, the relay will correctly model durable ingest and RELAY_INGEST emission semantics under the new Gossip V1 protocol.
