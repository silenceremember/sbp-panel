# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Release SBP 1.5.1

Desired outcome: publish a stable `v1.5.1` GitHub Release that fixes credential
QR compatibility without changing credential contents or protocol runtime
state.

Constraints and acceptance criteria:

- preserve the current native AmneziaWG credential and direct Xray link as the
  QR payloads; Copy keeps its existing selected text format;
- keep the change limited to QR generation, presentation, tests, documentation,
  version metadata, and release notes;
- set `Version` to `1.5.1` with `Prerelease = false`, and create the unused
  immutable tag `v1.5.1` only after all checks pass;
- publish the release from the existing workflow, verify its expected assets,
  and confirm it becomes `/releases/latest`.

Implementation context:

- QR generation and its HTTP handler live in `internal/panel/panel.go`;
- QR presentation lives in `internal/panel/web/app.css`;
- behavior is covered in `internal/panel/panel_test.go`;
- user-facing behavior is documented in `README.md` and `CHANGELOG.md`;
- release metadata lives in `internal/buildinfo/buildinfo.go`.

Progress:

- [x] Implement native AmneziaWG and direct Xray QR payload generation.
- [x] Add regression coverage and improve QR rendering size and sharpness.
- [x] Add the 1.5.1 version and changelog entry.
- [x] Run the complete applicable validation set and inspect the final diff.
- [ ] Commit, tag, push, and verify the GitHub Actions release.
- [ ] Remove this completed plan and verify the cleanup commit.

Decisions and discoveries:

- AmneziaVPN's QR importer accepts the native AmneziaWG profile directly; the
  `vpn://` wrapper remains useful for clipboard import and is left unchanged.
- Low QR error correction and integer module scaling match the official client
  approach and avoid unnecessary density and blurred module edges.

Validation:

- `test -z "$(gofmt -l .)"`
- `go test ./...`
- `go vet ./...`
- `node --check internal/panel/web/app.js`
- `node --check internal/panel/web/check.js`
- `bash deploy/test_scripts.sh`
- `bash deploy/changelog.sh 1.5.1`
- `git diff --check`

All listed validation commands passed on 2026-08-29. The inspected diff
contains only the QR fix, its tests and documentation, release metadata, and
this active plan.

Recovery path: do not create or push the tag unless validation and the intended
diff are clean. If publishing fails after the tag is pushed, fix the workflow
or publish a newer numeric version; never move or reuse `v1.5.1`.

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
