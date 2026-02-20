---
# Mission Template — aethos-relay Bead Execution

This template defines the standard execution flow for any relay server bead.

It relies on AGENTS.md for orchestration rules.
If any ambiguity arises, AGENTS.md takes precedence.

---

# Required Inputs

The caller must provide:

- Bead ID (example: relay-server-ws-bbolt-ttl)
- Short goal summary
- Optional constraints or special notes

---

# Execution Model

Follow the Agent Roles defined in AGENTS.md:

- Orchestrator Agent
- Implementation Agent (public repo only)

No agent may violate repository boundaries.

---

# STEP 1 — Create or Confirm Bead (Public Repo)

In `aethos`:

1. If bead does not exist:
   - Create bead with appropriate:
     - type (feature / bug / epic)
     - priority
     - labels (include relay, go)
     - summary (concise, outcome-focused)
     - description (goal + deliverables + acceptance criteria)

2. Create branch:
   ```
   git checkout -b bead/<bead-id>
   ```

If bead already exists, confirm branch exists or create it.

---

# STEP 2 — Create Matching Branch (Relay Repo)

In `aethos-relay`:

1. Create worktree for the bead:
   ```
   git worktree add .worktrees/<bead-id> -b bead/<bead-id>
   cd .worktrees/<bead-id>
   ```

---

# STEP 3 — Implement Scoped Work (Relay Repo Only)

All implementation happens in `aethos-relay`.

Guidelines:

- Maintain architectural separation.
- Keep payload opaque (base64 encoded).
- Keep protocol versionable via type strings.
- Use bbolt for durable storage (no SQLite, no CGO).
- Add or update tests as required.
- Keep tests deterministic.

---

# STEP 4 — Verification

Before pushing:

- Go build succeeds
- `go test ./...` passes
- `go vet ./...` passes
- No `.beads/*` files in relay repo

If verification fails:
- Fix before proceeding.

---

# STEP 5 — Push Branch + Open PR

In `aethos-relay`:

1. Push branch:
   ```
   git push -u origin bead/<bead-id>
   ```

2. Open PR into `main`:

   If GitHub CLI available:
   ```
   gh pr create \
     --base main \
     --head bead/<bead-id> \
     --title "<Concise Outcome-Oriented Title>" \
     --body "<What changed, how verified, any follow-ups>"
   ```

PR must include:
- Summary of behavior change
- Verification steps
- Any discovered follow-up beads

Mission is not complete until PR exists.

---

# STEP 6 — Close Bead (Public Repo)

In `aethos`:

```
bd close <bead-id>
bd sync
git restore .beads/issues.jsonl
git commit -m "Archive bead state (<bead-id>)"
git push
```

Verify:
- No unintended `.beads/*` files committed.
- Only expected archive commit present.

---

# Completion Contract

The mission is complete only when:

- Relay branch pushed
- PR to `main` exists
- Public bead closed
- Builds/tests verified
- Repository boundaries respected

If any step fails:
- Surface failure explicitly.
- Do not silently exit.

---

# Reporting

At completion, provide:

- Files changed (high-level summary)
- Build/test status
- PR link
- Bead status
- Suggested follow-up beads (if scope expanded)
---
