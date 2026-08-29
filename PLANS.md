# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Global profile revision updates and AmneziaWG MTU refresh

- Desired outcome: publish SBP 1.4.4 as a prerelease where every component
  exposes one global Update whenever stored profiles are older than its current
  generated configuration, and where existing AWG 3.1 profiles can be updated
  from MTU 1376 to 1280 without reinstalling the container or rotating keys.
- Constraints: no per-device Update action and no historical migration chain;
  use the existing profile generation as the current desired-revision marker;
  keep protocol upgrades distinct from profile-only refreshes; update all
  matching profiles atomically; never mutate server state during an MTU-only
  refresh.
- Acceptance criteria: discovery detects version or generation drift; generic
  metadata updates record both current fields; the AmneziaWG global Update
  rewrites every valid stored AWG profile to MTU 1280 while preserving keys and
  other settings; malformed input causes no partial database write; new and
  AWG-upgrade profiles start at the current generation; one component Update
  button drives the correct operation.
- Implementation context: component desired profile metadata originates in
  `internal/agent`; discovery and update orchestration live in
  `internal/panel`; atomic profile publication lives in `internal/store`; the
  dashboard dispatches by `update_kind` in `internal/panel/web/app.js`.
- Progress: implementation is complete; formatting, Go tests, vet, JavaScript
  syntax checks, deploy assertions, changelog extraction, and diff checks pass;
  release publication and CI verification remain.
- Validation: run formatting, Go tests, vet, JavaScript syntax checks, deploy
  script tests, changelog extraction, `git diff --check`, and both release CI
  workflows; verify v1.4.4 remains a prerelease and stable latest remains
  v1.3.3.
- Recovery: all profile refresh writes occur in one SQLite transaction and do
  not touch server runtime. Before publication, revert only the intended diff;
  after a failed immutable release, publish a newer numeric fix.

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
