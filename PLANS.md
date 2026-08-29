# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### AmneziaWG 3.1 client MTU prerelease fix

- Desired outcome: publish SBP 1.4.3 as a prerelease whose newly generated and
  component-update AmneziaWG profiles consistently request client MTU 1280.
- Constraints: keep the change client-profile-only; do not mutate existing
  stored profiles, server interfaces, keys, containers, or protocol settings;
  retain the current atomic AWG 3.1 update and rollback semantics.
- Acceptance criteria: both profile-generation paths use one MTU source of
  truth; tests prove generated profiles and the `vpn://` wrapper retain MTU
  1280; version and changelog agree; all applicable validation and release CI
  pass.
- Implementation context: `internal/agent/agent.go` provisions new peers,
  `internal/agent/amneziawg_component_update.go` reissues profiles during the
  AWG 3.1 component update, and `internal/panel/panel.go` wraps the native
  profile without transforming its contents.
- Progress: repository and upstream behavior inspected; both generation paths
  now use the shared MTU 1280 default, version 1.4.3 and its release notes are
  prepared, and the full local validation set has passed. Publication and CI
  observation remain.
- Validation: run formatting, Go tests, deploy script tests, vet, JavaScript
  syntax checks, changelog extraction, and `git diff --check`; inspect the
  intended diff and GitHub Actions result.
- Recovery: before publication, revert only the intended diff. After a failed
  release workflow, keep the immutable tag/release untouched, diagnose the
  failure, and publish a newer numeric version for any fix.

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
