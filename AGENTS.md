## Agent skills

### Issue tracker

Issues and specs live in this repo's GitHub Issues. See `docs/agents/issue-tracker.md`.

### Triage labels

Use the five default triage labels. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: use root `CONTEXT.md` and `docs/adr/`. See `docs/agents/domain.md`.

### Execution discipline

For multi-step ticket work, keep one bounded implementation path and drive it to a complete, reviewable ticket result before starting another seam.

- Define the whole-ticket acceptance criteria and dependency frontier before editing. A local seam is progress, not ticket completion.
- Use the smallest number of implementation, validation, and review passes that can establish acceptance. Reuse reliable evidence when the source snapshot is byte-identical; do not repeat completed gates.
- Before every long-running command, save a durable checkpoint containing the current worktree state, changed-file hashes, command, environment, and pending scope. Update the checkpoint with the exit status and counts immediately afterward.
- On a worker failure, inspect the terminal state and preserve the complete tracked and untracked diff before recovery. Resume the same worker once when the worktree and protocol remain recoverable. If recovery fails again, finish from the preserved snapshot directly; do not create an unbounded recovery chain or duplicate writer.
- Keep validation honest: a failed or interrupted gate remains failed or incomplete, and passing a local seam does not substitute for whole-ticket acceptance. Do not relabel an older snapshot's results as evidence for a changed snapshot.
- Review the final fixed ticket snapshot once along the required standards and specification axes. Fix concrete findings in the same bounded path, then perform the final acceptance gate; avoid repeated reviews and exploratory re-scans.
- Commit and publish only after the complete ticket passes its acceptance criteria. Keep later tickets blocked until their dependencies are complete.
