# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Global component Update patch releases 1.3.2 and 1.4.1

Desired outcome: assign protocol versions globally from Components rather than
per-device actions. Publish stable 1.3.2 and prerelease 1.4.1, retaining the
latter's distinct AmneziaWG 2.0 to 3.1 upgrade. Show Windows and Android client
downloads wherever both tested builds exist.

Constraints and acceptance criteria:

- Update is authenticated, CSRF-protected, and uses the global action lock.
- A metadata Update changes `protocol_version` atomically for every profile of
  that component and does not rewrite profiles, keys, rooms, or containers.
- Xray, XHTTP, AmneziaWG, and each routing provider remain separate scopes. No
  device row exposes Update and no historical routing room is migrated.
- In 1.4.1, an installed AmneziaWG 2.0 component keeps the existing full 3.1
  upgrade and key rotation. An installed 3.1 component uses metadata Update.
- Versions come from trusted component discovery, never request input. Client
  downloads target verified exact GitHub assets.
- Both releases require the full validation set, matching immutable tags,
  passing release workflows, correct release categories, and archive audits.

Implementation context: the agent exposes a separate machine-readable profile
version. Panel discovery compares it with SQLite metadata and selects either a
metadata action or the existing AWG upgrade. The store updates one method with
one statement. UI reuses component actions and pending-state guards.

Validation: gofmt, uncached Go tests, vet, both JavaScript syntax checks,
`deploy/test_scripts.sh`, `git diff --check`, Linux amd64 CGO-disabled build,
and GitHub release archive/category audit.

Recovery: metadata assignment has no external side effects. The AWG upgrade
keeps its snapshot and rollback path. Existing tags are never moved.

Progress:

- [x] Implemented, validated, published, and audited stable 1.3.2.
- [x] Carried the common behavior to main while preserving the AWG 3.1 upgrade.
- [x] Completed focused and full Go tests, vet, JavaScript syntax checks,
  deploy assertions, formatting and diff checks, responsive UI inspection, and
  a CGO-disabled Linux amd64 build.
- [ ] Publish and audit prerelease 1.4.1.

## ExecPlan format

For multi-step, architectural, destructive, security-sensitive, or long-running
work, replace `No active execution plans.` with a plan that includes:

- the desired observable outcome;
- constraints and acceptance criteria;
- exact implementation context;
- progress, decisions, and discoveries kept current while work proceeds;
- validation commands and their results;
- a recovery path for partial failure;
- the final outcome and any remaining risks.

Remove a completed plan after its outcome is recorded and the work is fully
verified. Do not use this file as a historical changelog.
