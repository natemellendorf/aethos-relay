# ADR-0001: Gossip V1 federation canonicalization adopts Option B

- Status: Accepted
- Date: 2026-03-20
- Decision owners: aethos-relay maintainers

## Context

The relay had mixed documentation and residual language from two incompatible relay federation models:

1. Legacy JSON `relay_forward.message` / `relay_ack` framing.
2. Gossip V1 binary HELLO/SUMMARY/REQUEST/TRANSFER/RECEIPT framing with canonical content-addressed objects.

Implementation has already migrated to Gossip V1 binary framing for relay-to-relay transport, but docs still described legacy frames in several places. In addition, TAR/forwarding strategy knobs existed in runtime configuration while not being wired into active Gossip V1 scheduling.

## Decision

Adopt **Option B** as the canonical federation profile for `aethos-relay`:

- **Inter-domain federation (authoritative):** relay-to-relay replication uses Gossip V1 binary frames and canonical transfer objects.
- **Intra-domain fleet coordination (separate concern):** local operator policy/scheduling features (including TAR batching/padding/cover and score-tuned forwarding) are not part of the inter-domain wire contract and are currently de-scoped from active relay-to-relay gossip scheduling in this repo.

## Rationale

- Matches implemented transport and parser/validator behavior.
- Removes contradictory `relay_forward.message` model language.
- Keeps federation contract small, testable, and content-addressed.
- Separates public interoperability guarantees from private fleet-optimization policy.

## Consequences

### Positive

- One authoritative relay-to-relay contract path.
- Clear migration/deprecation stance for legacy `relay_forward.message` documentation.
- Conformance tests align to accepted/rejected TRANSFER semantics under Gossip V1.

### Trade-offs

- Existing TAR/forwarding runtime flags remain parsed/validated but are explicitly documented as not currently active in relay-to-relay scheduling.
- Additional bead(s) will be needed to fully activate and verify TAR/forwarding policy in the canonical path.

## Non-goals

- This decision does not define private fleet policy internals.
- This decision does not change client-facing Gossip V1 semantics.
