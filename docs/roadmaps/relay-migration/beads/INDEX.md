# Relay Migration Bead Index

## Deterministic execution order

1. `relay_phase_1`
2. `relay_phase_2`
3. `relay_phase_3`
4. `relay_phase_4`
5. `relay_test_hardening_1`

## Dependency graph

- `relay_phase_1` depends on: none
- `relay_phase_2` depends on: `relay_phase_1`
- `relay_phase_3` depends on: `relay_phase_1`, `relay_phase_2`
- `relay_phase_4` depends on: `relay_phase_3`
- `relay_test_hardening_1` depends on: `relay_phase_4`

Agents must only start a phase when all depends_on are satisfied.

## Circular dependency verification

No circular dependencies exist because each phase only depends on earlier phases in the strict forward order (1 → 2 → 3 → 4 → relay_test_hardening_1), and no phase points back to itself or to any later phase.
