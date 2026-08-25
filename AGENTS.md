# AGENTS.md

This file applies to the entire repository. A more specific `AGENTS.md` or
`AGENTS.override.md` in a subdirectory may refine these rules for that subtree.

## Project

SBP, Simple Bridge Panel, is a small self-hosted Go application for preparing
and managing a fresh VPN server. It provides a web panel, a privileged local
agent, SQLite state, component lifecycle management, access groups, credentials,
traffic counters, and update recovery.

Supported production target:

- fresh Ubuntu 24.04 LTS;
- Linux amd64;
- systemd and apt;
- a directly reachable public IPv4 address;
- root or sudo access for installation.

Do not broaden support claims without implementation and tests.
SBP 1.x starts from a fresh server. Do not add pre-1.0 migrations, adoption,
upgrade payloads, or cleanup paths.

## Repository map

- `cmd/sbp-panel`: executable entrypoint and command modes.
- `internal/panel`: unprivileged HTTPS UI/API, authentication, group access
  reconciliation, and traffic persistence.
- `internal/agent`: privileged allowlisted operations for systemd, apt, sysctl,
  Docker, credentials, metrics, and updates.
- `internal/store`: SQLite schema, queries, and transactions.
- `internal/panel/web`: embedded HTML, CSS, JavaScript, and SVG assets.
- `internal/buildinfo`: product name, repository, and release version.
- `deploy`: bootstrap, uninstall, and shell lifecycle assertions.
- `install.sh`: public one-line installer.
- `.github/workflows/release.yml`: CI and release bundle pipeline.
- `README.md`: concise user-facing overview and commands.
- `PLANS.md`: format and live record for complex execution plans.

## Sources of truth

Use this order when facts disagree:

1. current code and tests;
2. checked-in configuration and release workflow;
3. `README.md`;
4. comments, old plans, and historical discussion.

Never invent a protocol, version, port, feature, compatibility claim, or
recovery guarantee. Verify it in the current tree.

## Working process

1. Read this file, `PLANS.md`, the relevant code, and `git status` before
   editing.
2. Preserve unrelated and pre-existing user changes.
3. For a small, local, low-risk change, implement directly.
4. For multi-step, architectural, destructive, security-sensitive, or
   long-running work, create or update an ExecPlan in `PLANS.md` first.
5. Make the smallest coherent change that fully solves the problem.
6. Validate in proportion to risk, then inspect the final diff.
7. Update the plan's progress, decisions, discoveries, and outcome while the
   work proceeds. Do not leave a completed plan claiming unfinished work.
8. Do not commit, tag, publish a release, or mutate a live server unless the
   user explicitly asks.

An ExecPlan must state the desired outcome, constraints, observable acceptance
criteria, exact implementation context, validation commands, and recovery path.
It must be usable by a contributor who has only the repository and the plan.

## Safety invariants

- Treat the privileged agent as a root-equivalent surface.
- Never commit real passwords, cookies, private keys, UUID credentials, session
  tokens, server IP addresses, or production database contents.
- Never adopt or remove external Docker, tuning, containers, images, or files
  merely because their names resemble SBP resources.
- Destructive operations must use exact targets, ownership evidence, strict
  error handling, and post-action verification.
- Panel-only uninstall must not remove `/opt/vpn-panel-managed`, Docker,
  managed service containers or images, or network tuning.
- Component uninstall must respect dependency order and refuse unsafe removal.
- Keep install, update, retry, rollback, and uninstall operations idempotent.
- Persist desired state before or atomically with external mutations. If an
  operation spans SQLite and system state, define compensation or reconciliation.
- Preserve temporary rollback artifacts until replacement services pass health
  checks, then remove only proven SBP-owned staging and rollback paths.
- Every persistent file, log, cache, backup, meter, and runtime artifact must
  have an explicit owner, size or retention bound, cleanup path, and recovery
  purpose. Do not add unbounded logs, archives, caches, or polling state.
- Keep generated binaries, databases, credentials, logs, caches, coverage,
  editor state, temporary files, and build output out of the tracked tree.
- Do not weaken authentication, CSRF, rate limits, secret permissions, service
  isolation, checksum checks, or update rollback without an explicit security
  rationale and tests.

## Runtime invariants

- The panel process is unprivileged. Privileged actions go through the local
  agent socket and an allowlisted API.
- Group expiration is desired access state and must be reconciled with runtime
  credentials and routing rooms.
- Xray credential changes should use its runtime API. Do not reintroduce shared
  container restarts for ordinary add, toggle, or remove operations.
- AmneziaWG peer changes use `awg syncconf` and must not restart the shared
  container for ordinary add, toggle, or remove operations.
- Distinct protocol variants must use distinct component IDs, container names,
  managed directories, public ports, runtime APIs, and traffic namespaces.
  Installing, updating, or removing one variant must not mutate another.
- Routing integrations use one independently managed room per device. Room
  traffic is attributed to that device and rolled up into its group total.
- Deleting a group must clean its devices, credentials, traffic rows, and saved
  routing rooms, while reporting any external cleanup that could not be proven.
- Traffic data covers only the current UTC month. Xray and AmneziaWG can be
  measured per device; routing room traffic is aggregated per group/provider.
- Traffic counters are operational estimates, never billing-grade claims.
- Persistent managed containers use Docker logging disabled unless a bounded,
  documented diagnostic path requires otherwise.

## UI conventions

- User-facing interface text is English.
- The accent color is `#EF9B47`.
- Keep the interface compact, responsive, and consistent with existing
  components. Reuse shared button, dialog, notification, table, and loading
  patterns instead of adding one-off styling.
- Use notifications for short operation results. Do not add page-darkening
  overlays to notifications.
- Preserve keyed, in-place dashboard updates and request generation guards.
  Avoid full redraws, duplicate fetches, stale response application, and rapid
  action state teleportation.
- Keep tables stable with predictable columns. Do not redesign established UX
  unless the task requires it.
- Use ordinary hyphens instead of em dashes in repository prose and UI copy.

## Implementation conventions

- Prefer clear, explicit Go over abstraction that only shortens line count.
- Consolidate repeated policy in small registries or helpers when it produces
  one source of truth.
- Keep HTTP errors machine-readable and user-facing messages specific.
- Use transactions for related database writes.
- Use atomic staging and rename for configuration, binary, and state files.
- Bound request bodies, downloads, subprocess duration, concurrency, retries,
  in-memory tables, and polling intervals.
- For Docker operations, distinguish container-not-found and image-not-found
  from daemon or permission failures. Fail closed on unknown inventory state.
- Do not silently ignore cleanup failures when doing so can leave working
  credentials, restartable containers, or lost ownership state.
- Comments should explain non-obvious constraints and failure semantics, not
  restate the code.

## Validation

Run the smallest relevant checks while iterating, then the complete applicable
set before declaring a substantial change done:

```bash
test -z "$(gofmt -l .)"
go test ./...
bash deploy/test_scripts.sh
git diff --check
```

When relevant and available, also run:

```bash
go vet ./...
node --check internal/panel/web/app.js
node --check internal/panel/web/check.js
```

Additional requirements:

- Store/schema work needs fresh-schema and transaction tests.
- HTTP/API work needs authentication, authorization, CSRF, status, and error
  contract tests where applicable.
- Lifecycle work needs success, retry, interruption, rollback, ownership, and
  cleanup tests.
- UI work needs at least syntax checking plus manual verification of the
  affected interaction at normal and narrow widths.
- Release/install changes need `deploy/test_scripts.sh` and the clean lifecycle
  assumptions in the release workflow reviewed together.

Do not claim checks passed when they were not run. State the reason and the
remaining risk.

## Release discipline

- The version and GitHub release category sources are `Version` and
  `Prerelease` in `internal/buildinfo/buildinfo.go`.
- Tags use `vX.Y.Z` and must match the numeric source version exactly.
- `X` is a compatibility-breaking release, `Y` is a feature release, and `Z`
  is a compatible fix or polish release.
- `Prerelease = true` publishes the numeric tag as a GitHub prerelease and must
  keep it out of `/releases/latest`; it is a usable build under observation.
- Promoting an observed build changes the existing GitHub Release category. It
  never moves or recreates the tag. Publish a newer numeric version for fixes.
- `1.0.0` is reserved for a deliberately reviewed compatibility baseline, not
  merely the next feature release.
- A release requires explicit user authorization, a clean intended diff, the
  full applicable checks, a version bump, a matching commit and tag, and review
  of the GitHub Actions result.
- The release archive is an explicit allowlist containing only `LICENSE`,
  `NOTICE`, `bootstrap.sh`, `uninstall.sh`, and `sbp-panel-linux-amd64`.
  Repository source, coordination, CI, local tooling, and the public installer
  entrypoint must not be copied merely because they are tracked.
- Never move or reuse an existing release tag.

## Definition of done

A change is done only when:

- the requested observable behavior works;
- safety and ownership invariants still hold;
- relevant success and failure paths are covered;
- the applicable checks pass;
- documentation reflects user-visible behavior;
- temporary files and debug instrumentation are absent;
- `git diff` contains only intended changes;
- an active ExecPlan, if used, records the final outcome and remaining risks.
