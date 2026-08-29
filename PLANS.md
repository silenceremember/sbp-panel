# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Release SBP 1.5.0

Desired outcome: publish the verified global component-update work as stable
release `v1.5.0`, with matching source version, concise release notes, commit,
tag, GitHub Release assets, and successful release workflow.

Constraints and acceptance criteria:

- include only the intended routing reconciliation, Xray profile refresh,
  tests, documentation, version, and changelog changes;
- keep `Prerelease = false` and publish `v1.5.0` as the latest stable release;
- preserve Xray UUIDs/runtime state and the forward-only routing retry behavior;
- run the complete local validation set before committing and tagging;
- never reuse or move an existing tag;
- push the release commit and matching tag, then verify GitHub Actions and the
  resulting release category/assets.

Implementation context: release metadata lives in
`internal/buildinfo/buildinfo.go` and `CHANGELOG.md`; `.github/workflows/release.yml`
builds the allowlisted archive and publishes the tag release.

Progress:

- [x] Verified the implementation checks before release preparation.
- [x] Added the 1.5.0 version and changelog section.
- [x] Re-ran all applicable local checks and inspected the intended diff.
- [ ] Commit, tag, and push main plus `v1.5.0`.
- [ ] Verify the release workflow and published stable release assets.

Validation commands:

```bash
test -z "$(gofmt -l .)"
go test ./...
go vet ./...
node --check internal/panel/web/app.js
node --check internal/panel/web/check.js
bash deploy/test_scripts.sh
git diff --check
```

Recovery path: before push, correct the commit or recreate only the local tag.
After push, never move `v1.5.0`; publish a newer numeric patch for any fix. If
CI fails before release publication, diagnose and fix forward in a new version
when the tag has already become public.

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
