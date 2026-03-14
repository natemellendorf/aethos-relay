---
id: relay_ws_binary_transport
title: "aethos-relay — Align WebSocket transport to carry Gossip V1 binary stream frames"
depends_on: []
external_dependencies: []
---

Bead: aethos-relay — Align WebSocket transport to carry Gossip V1 binary stream frames

Problem


The upgraded iOS client now sends Gossip V1 bytes over the stream/session path, but relay connections fail with:


invalid_protocol_frame: binary_frame_not_allowed


This indicates the relay WebSocket transport is still enforcing a legacy text-only or non-binary framing contract and is rejecting Gossip V1 transport bytes before protocol decode occurs.


We need to align the relay transport layer so WebSocket can act as a valid Gossip V1 stream bearer.


Goals


1. allow relay WebSocket transport to accept binary frames for Gossip V1
2. route accepted binary payloads into the Gossip V1 stream/session adapter
3. remove or isolate legacy text-frame assumptions from the migrated relay path
4. prove upgraded iOS client can complete at least HELLO over relay WebSocket


Challenge assumptions


- Do not wrap Gossip V1 in ad hoc JSON unless explicitly required by a separate design decision.
- Do not preserve text-only restrictions in the migrated Gossip V1 path.
- Do not treat this as a protocol-core failure; this is a transport embedding issue.
- Do not leave both binary and legacy text protocol paths ambiguously active in the same migrated route.


Implementation tasks


1. Audit current relay WebSocket transport
Identify where binary frames are rejected and where text-frame assumptions are enforced.


2. Allow binary frames on the migrated Gossip V1 WebSocket path
Update the relay WebSocket transport so binary messages are accepted for Gossip V1 connections.


3. Route binary payloads into the Gossip V1 stream/session adapter
Feed received binary bytes into the same stream/session handling path used by Gossip V1.


4. Preserve fail-closed behavior
If binary payloads are malformed or violate stream framing, terminate the encounter/session cleanly.


5. Add focused tests
Add relay tests proving:
- binary WebSocket frames are accepted on the Gossip V1 path
- valid binary payload reaches session decode
- malformed binary payload is rejected correctly
- upgraded peer can complete HELLO round-trip


Acceptance criteria


- relay no longer rejects Gossip V1 binary WebSocket frames on the migrated path
- relay feeds binary WebSocket payloads into the Gossip V1 session adapter
- upgraded iOS client can at least complete HELLO over relay WebSocket
- legacy text-only behavior is not used by the migrated Gossip V1 route
