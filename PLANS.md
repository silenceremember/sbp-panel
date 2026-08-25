# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Device update cleanup (`v1.3.1`) and AWG 3.1 prerelease (`v1.4.0`)

Desired outcome: first publish stable `v1.3.1` with device-level Update and
automatic Whitelist Bypass migration removed, while retaining the proven AWG
2.0 and AmneziaVPN 4.8.21.0 combination. Then publish an observed `v1.4.0`
GitHub prerelease that provisions
AmneziaWG 3.1-compatible server and device profiles and recommends the pinned
AmneziaVPN 5.0.1.5 client. Fresh installs receive the same tested combination.
An installed AmneziaWG component is upgraded only through one component-level
Update action, which rebuilds the managed container and reissues the server and
every device identity as one coordinated operation. Devices have no individual
profile Update action.

Constraints and acceptance criteria:

- keep stable `v1.3.0` and its tag unchanged;
- keep every AWG 3.1 engine, profile, client, and component-update change out of
  `v1.3.1`;
- use one pinned, verified server image and one pinned client release rather
  than an unbounded `latest` reference;
- derive AWG 3.1 fields from the matching upstream sources, but use a
  conservative performance preset: header protection enabled, S1-S4 at least
  12, narrow headers, RandomTrailers and DisableCookies off, and no optional
  content-padding or timing ranges;
- never relabel an AWG 2.0 profile as 3.1 without applying matching server
  configuration;
- stage and validate the replacement image, server identity, server config,
  container, and all newly issued client profiles before publishing any
  profile; on an apply or database failure, restore the prior container,
  configuration, metadata, and saved profiles;
- retain the existing no-restart peer update path for ordinary device changes;
- clearly warn that component Update revokes all old AWG keys and requires
  every user to import the newly issued profile;
- remove the device-level Update UI and route; cover fresh provisioning,
  successful component update, failed replacement, failed profile publication,
  rollback, retry, and already-current behavior with tests;
- keep new Whitelist Bypass devices on one dedicated room per device, but remove
  automatic shared-room migration; existing shared-room devices are recreated
  manually by the administrator;
- document the experimental client/server combination and known upstream
  regression risk without claiming it stable.

Implementation context:

- build identity: `internal/buildinfo/buildinfo.go`;
- AWG image, provisioning, settings, rendering, and runtime apply logic:
  `internal/agent`;
- all-device update orchestration and profile persistence: `internal/panel`
  and `internal/store`;
- client links and Update interaction: `internal/panel/web`;
- public behavior and pinned versions: `README.md`.

Progress and decisions:

- [x] Confirmed the worktree is clean and `v1.4.0` is unused.
- [x] Kept `v1.3.0` stable; this feature uses a new numeric prerelease.
- [x] Published stable `v1.3.1` with only the device Update and migration
  cleanup; AWG remains at protocol 2.0 with AmneziaVPN 4.8.21.0. Release CI and
  the release allowlist, digest, size, and updater metadata were verified.
- [x] Verified the AWG 3.1 config keys, client serialization, pinned engine tag,
  and the AmneziaVPN 5.0.1.5 Windows asset against the upstream releases and
  sources.
- [x] Chose conservative AWG 3.1 defaults instead of copying every experimental
  upstream default.
- [x] Implemented component-level rebuild, key rotation, atomic profile
  publication, durable rollback state, retry/recovery, and removal of the
  per-device Update path.
- [x] Updated the UI, client links, version labels, and documentation.
- [x] Ran the complete validation set and inspected the intended diff.
- [ ] Commit, tag, publish the prerelease, monitor CI, and audit its assets.

Validation:

```bash
test -z "$(gofmt -l .)"
go test -count=1 ./...
go vet ./...
bash deploy/test_scripts.sh
node --check internal/panel/web/app.js
node --check internal/panel/web/check.js
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/sbp-panel
git diff --check
```

`v1.3.1` validation result: all listed checks passed on 2026-08-25,
including the Linux amd64 build and uncached Go tests.

Recovery path: before changing a live AWG component, preserve its exact managed
configuration and metadata. If candidate validation, runtime apply, or profile
publication fails, reapply the preserved configuration, verify the old
interface is healthy, discard staged files, and report a retryable error. The
release tag is created only after local validation; a failed release build is
fixed in a newer numeric prerelease rather than by moving or reusing a tag.

Released work and historical decisions belong in Git history and GitHub
releases, not in this file.

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
