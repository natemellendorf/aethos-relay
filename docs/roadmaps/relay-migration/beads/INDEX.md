# Relay Migration Bead Index

## Deterministic execution order

1. `relay_phase_1`
2. `relay_phase_2`
3. `relay_phase_3`
4. `relay_phase_4`
5. `relay_test_hardening_1`
6. `relay_ws_binary_transport`

## Dependency graph

- `relay_phase_1` depends on: none
- `relay_phase_2` depends on: `relay_phase_1`
- `relay_phase_3` depends on: `relay_phase_1`, `relay_phase_2`
- `relay_phase_4` depends on: `relay_phase_3`
- `relay_test_hardening_1` depends on: `relay_phase_4`
- `relay_ws_binary_transport` depends on: none

Agents must only start a phase when all depends_on are satisfied.

## Circular dependency verification

No circular dependencies exist because each listed bead only depends on earlier beads or none at all (relay_phase_1 → relay_phase_2 → relay_phase_3 → relay_phase_4 → relay_test_hardening_1, with relay_ws_binary_transport independent), and no bead points back to itself or to any later dependent bead.
