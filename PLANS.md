# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Promote v1.4.4 to stable

- Desired outcome: promote the already observed v1.4.4 build from GitHub
  prerelease to stable without moving or recreating its tag.
- Constraints: keep the immutable v1.4.4 tag and assets unchanged; set the
  checked-in release category to stable; keep exactly one changelog section for
  1.4.4; do not publish another numeric version.
- Acceptance criteria: local and main-branch checks pass, the v1.4.4 GitHub
  Release is non-draft and non-prerelease, `/releases/latest` resolves to
  v1.4.4, and the repository is clean.
- Implementation context: `Prerelease` in `internal/buildinfo/buildinfo.go` is
  the checked-in category source; GitHub Release category is changed in place.
- Progress: source synchronization is complete; formatting, Go tests, deploy
  assertions, changelog extraction, and diff checks pass; GitHub promotion and
  final verification remain.
- Validation: run formatting, Go tests, deploy script tests, changelog
  extraction, and `git diff --check`; then verify main CI and GitHub release
  metadata.
- Recovery: before GitHub promotion, revert the intended source commit if
  validation fails; after promotion, restore prerelease categorization in place
  if verification exposes a release-blocking problem.

## ExecPlan format

For multi-step, architectural, destructive, security-sensitive, or long-running
work, replace `No active execution plans.` with a plan that includes:

- the desired observable outcome;
- constraints and acceptance criteria;
- exact implementation context;
- progress, decisions, discoveries kept current while work proceeds;
- validation commands and their results;
- a recovery path for partial failure;
- the final outcome and any remaining risks.

Remove a completed plan after its outcome is recorded and the work is fully
verified. Do not use this file as a historical changelog.
