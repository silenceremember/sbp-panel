# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Release SBP 1.3.0

- Desired outcome: publish the current intended profile-update feature set as stable GitHub release `v1.3.0`.
- Constraints: preserve all intended working-tree changes, include no secrets or unrelated artifacts, keep pinned component versions truthful, and do not reuse an existing tag.
- Acceptance criteria: the Amnezia fresh-install behavior is verified from code and upstream metadata; `Version` is `1.3.0` with `Prerelease = false`; documentation matches; full Go, shell, JavaScript, vet, formatting, and diff checks pass; the intended diff is committed, tagged, pushed, and the GitHub Actions release completes successfully.
- Implementation context: `internal/buildinfo/buildinfo.go`, Amnezia constants and installer in `internal/agent`, `README.md`, `.github/workflows/release.yml`, and the current feature diff.
- Validation: `gofmt -l .`, `go test ./...`, `go vet ./...`, `bash deploy/test_scripts.sh`, JavaScript syntax checks, `git diff --check`, release workflow/tag inspection, and GitHub Actions/release verification.
- Recovery: before pushing, fix or abort without creating a tag; after pushing a bad release, do not move or reuse `v1.3.0` - mark it prerelease if appropriate and publish fixes under a newer numeric version.
- Progress: release audit and final diff review completed. Official issue review found active Windows transport, upgrade, split-tunnel, and AWG 2.0 regression reports for AWG 3.1 with AmneziaVPN 5.0.1.5, so stable 1.3.0 remains pinned to the tested `amneziawg-go 3.1.20260814` engine in 2.0 profile mode and recommends AmneziaVPN 4.8.21.0. Version bumped. The in-app browser blocked the localhost fixture, so UI verification is limited to syntax, route/interaction tests, and responsive CSS inspection; final full validation is in progress.

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
