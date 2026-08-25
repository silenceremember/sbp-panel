# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Releases 1.3.3 and 1.4.2 - per-device routing traffic

- Desired outcome: attribute each managed Whitelist Bypass room's current-month
  traffic to its owning device, display that value in the ordinary device
  traffic column, roll it into the group total, remove the separate WB/VK group
  traffic detail line, publish stable release `v1.3.3` from the `v1.3.2`
  stable line, and publish prerelease `v1.4.2` from the AmneziaWG 3.1 line.
- Constraints: only dedicated room containers whose exact name proves both the
  group and device IDs are attributable; do not migrate or guess ownership for
  historical group-only rooms; keep traffic explicitly operational rather than
  billing-grade; do not include the prerelease AmneziaWG 3.1 changes in 1.3.3.
- Acceptance criteria: agent metrics include the proven device ID for every
  dedicated routing room; the panel validates that mapping against its device
  state, persists it as device traffic, and exposes it through the existing
  `managed_devices` response; the dashboard no longer renders provider detail
  badges; group totals remain the sum of device protocol totals; component and
  README copy describe per-device tracking.
- Implementation context: `internal/agent/agent.go` collects Docker NetIO;
  `internal/panel/traffic.go` translates privileged metrics into managed IDs;
  `internal/store/store.go` persists device and group rollups;
  `internal/panel/web/app.js` renders live traffic; `README.md` documents the
  scope. Tests live alongside these packages.
- Progress: implementation and focused tests pass on both release lines; full
  stable and prerelease validation, version commits, tags, publication, and
  release audits remain.
- Validation: run formatting, all Go tests, lifecycle shell tests, Go vet, both
  JavaScript syntax checks, and `git diff --check`; inspect each release-line
  diff against its predecessor, then review both GitHub Actions release results.
- Recovery: before tagging, revert the feature commit or reset only the release
  branch to `v1.3.2` or `v1.4.1`; after publication, never move either tag and
  publish a newer patch for any correction.

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
