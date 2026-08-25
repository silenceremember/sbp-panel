# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Global component Update patch releases 1.3.2 and 1.4.1

Desired outcome: protocol versions are assigned globally from the Components
table instead of exposing Update on individual devices. Publish the behavior as
stable 1.3.2 from the 1.3.1 line and as prerelease 1.4.1 from the 1.4.0 line,
retaining the latter's AmneziaWG 3.1 component update. Client applications that
support both Windows and Android show tested links for both platforms.

Constraints and acceptance criteria:

- Update is an authenticated, CSRF-protected component action and shares the
  existing global pending-action lock with installs, removals, and updates.
- A normal component Update atomically records the trusted component's current
  protocol version for every device using that method. It does not rewrite a
  profile, rotate a key, change a room, or restart a container.
- Xray TCP, XHTTP, AmneziaWG, and each whitelist provider are separate update
  scopes. The action is offered only for an installed SBP-managed component
  with at least one mismatched device profile version.
- Stable AmneziaWG 2.0 metadata assignment does not rewrite or rotate profiles.
- In 1.4.1, an installed AmneziaWG 2.0 component continues to use the existing
  explicit component upgrade to 3.1, including its documented all-profile key
  rotation. An already current 3.1 component needs only the metadata action.
- No device row exposes Update. Historical shared whitelist rooms are not
  migrated or recreated by this feature.
- Current component and protocol versions come from checked-in or inspected
  trusted state, never from request input.
- The dashboard retains its keyed in-place rendering behavior.
- Client direct-download links target exact verified GitHub release assets.
- 1.3.2 contains no AmneziaWG 3.1 component lifecycle. 1.4.1 retains and tests
  the existing component-level AmneziaWG 2.0 to 3.1 update separately.
- Both releases require the full repository validation set, clean intended
  diffs, matching version metadata, commits and immutable numeric tags, passing
  tag workflows, and audited GitHub release categories and archives.

Implementation context:

- Add one registry-driven panel operation that maps an installed managed
  protocol component to its device method and current profile version, then
  updates all matching metadata in one SQLite statement. Reuse the current
  component action and notification patterns; do not restore the former
  `/api/devices/{id}/profile` or `/v1/profiles` routes.
- Expose a machine-readable profile version in agent discovery separately from
  the display version.
- Prepare 1.3.2 on a maintenance branch based on v1.3.1. Carry the common
  feature to main, adapt it to the 1.4 AmneziaWG renderer, then publish 1.4.1 as
  a prerelease without moving any existing tag.

Validation:

- `test -z "$(gofmt -l .)"`
- `go test -count=1 ./...`
- `go vet ./...`
- `node --check internal/panel/web/app.js`
- `node --check internal/panel/web/check.js`
- `bash deploy/test_scripts.sh`
- `git diff --check`
- Linux amd64 CGO-disabled build and release archive/category audit.

Recovery: metadata assignment is one SQLite statement and has no external side
effects. The existing AmneziaWG 3.1 component update keeps its independent
snapshot and rollback path. Existing numeric tags are never moved; a failed
published build receives a newer numeric version.

Progress:

- [x] Confirmed the stale `refresh available` label comes from empty metadata
  left after the device Update route was removed in 1.3.1.
- [x] Confirmed the requested action is global per component, not per device;
  the removed 1.3.0 device refresh implementation must remain removed.
- [x] Added the requirement for separate Windows and Android client links where
  both tested builds exist.
- [x] Implemented the common behavior on the 1.3.1 maintenance line. Focused
  store, panel, agent, authorization, CSRF, UI, and asset-link tests pass.
- [x] Full 1.3.2 validation passed: formatting, uncached Go tests, vet, both
  JavaScript syntax checks, deploy assertions, diff check, and Linux amd64
  CGO-disabled build.
- [ ] Publish and audit stable 1.3.2.
- [ ] Carry the behavior to main and adapt it to the AmneziaWG 3.1 renderer.
- [ ] Publish and audit prerelease 1.4.1.

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
