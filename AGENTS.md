# Agents Operating Guide (aethos-relay)

This repository is the public WebSocket relay server with bbolt TTL persistence. Work is organized using beads in the public `aethos` repo.

This file defines stable rules for agent orchestration.

## Repositories and Boundaries

- Public repo: `aethos`
  - Owns beads, bead lifecycle, and any core protocol changes.
- Public repo: `aethos-relay`
  - Owns the WebSocket relay server implementation.

Hard boundary rules:
- Agents working in `aethos-relay` must not modify `aethos`.
- Agents working in `aethos` must not modify `aethos-relay` implementation.
- Do not commit `.beads/*` in any public repo.

## Role Model

### Orchestrator Agent (Primary)
Owns:
- Creating/closing beads in `aethos`
- Creating matching branches in both repos
- Spawning and coordinating background agents
- Ensuring repo boundary compliance
- Verifying builds/tests
- Opening PRs in `aethos-relay`
- Ensuring `.beads` hygiene is maintained

Only the Orchestrator may:
- Close beads
- Push public bead archive commits
- Open PRs (unless explicitly delegated)

### Implementation Agent (Public Repo Only)
Owns:
- Implementing the scoped change in `aethos-relay`
- Adding tests and fixtures as needed
- Running Go build + tests
- Committing and pushing changes to the bead branch

Must not:
- Close beads
- Modify aethos repo
- Touch `.beads/*`

## Standard Workflow (Applies to All Beads)

1. Bead creation happens in `aethos` (public repo).
2. Create matching branches:
   - `bead/<bead-id>` in `aethos`
   - `bead/<bead-id>` in `aethos-relay`
3. Implement the scoped work in `aethos-relay`.
4. Verify:
   - Go build succeeds
   - Unit tests pass (`go test ./...`)
   - `go vet ./...` passes
5. Push the branch and open a PR into `main`.
6. **Monitor required CI/jobs after PR is opened.** Do not proceed to step 7 until all required checks are green.
7. Close the bead in `aethos` and keep bead files clean.

## Worktree Discipline

All agent work MUST run in dedicated git worktrees to ensure clean separation.

### Canonical Workflow

1. **Create worktree** before starting any agent work:
   ```bash
   git worktree add .worktrees/<bead-id> -b bead/<bead-id>
   cd .worktrees/<bead-id>
   ```

2. **Run agent work** in the worktree directory.

3. **Cleanup** when done:
   ```bash
   cd /path/to/main
   git worktree remove .worktrees/<bead-id>
   git branch -d bead/<bead-id>
   ```

### Safety Rules

- **Never** develop directly in the main checkout
- **Always** use `git worktree add` for new bead branches
- Worktrees are stored in `.worktrees/<bead-id>/` convention
- Each worktree has its own branch: `bead/<bead-id>`

## Mandatory Git Safety Rules

All beads must follow these safety rules without exception:

1. **All beads must run in worktrees**
   - Never develop directly in the main checkout

2. **All beads must branch from latest main**
   - Ensure branch is descendant of `origin/main`

3. **Agents must stop immediately if validation fails**
   - Do not proceed with implementation if build/test fails

## Communication and Handoffs

Background agents should report back to the Orchestrator with:
- What was changed (file list)
- Verification status (build/tests)
- Any deviations or risks
- Suggested follow-up beads if scope expanded

## Definition of Done

A task is not complete until:
- All changes are committed on `bead/<bead-id>`
- Branch is pushed to origin
- PR to `main` exists with a meaningful title and body
- Build/tests are green
- **All required PR jobs/checks are passing**
- **Do not report mission success until all required checks are green.**
- **Do not close bead until PR checks are passing.**

PR body should include:
- What changed
- How it was verified (tests/build)
- Follow-ups / next beads (if discovered)
