# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Replace the SBP Xray REALITY target proven incompatible on the affected route

Desired outcome: fresh SBP Xray TCP and XHTTP installations use a REALITY
target that passes traffic on the affected VPS and client route without
weakening container isolation, changing the pinned core, or altering runtime
credential management.

Constraints and acceptance criteria:

- Change only the generated REALITY target, allowed server name, persisted SNI,
  and their default metadata fallback.
- Keep Xray 26.3.27, the ordinary Docker bridge, the unprivileged read-only
  container profile, public-to-container port mapping, runtime API users, and
  traffic accounting unchanged.
- Both generated server variants and their VLESS links must use the same target
  name.
- A fresh TCP installation must pass small requests and sustained traffic from
  v2rayN. XHTTP must receive the same target correction and retain its distinct
  path, port, and runtime namespace.

Implementation context:

- REALITY configuration and persisted client metadata are generated in
  `internal/agent/agent.go`.
- VLESS links and legacy metadata fallback are generated in
  `internal/agent/xray_variants.go`.
- Regression coverage belongs with the existing Xray variant tests in
  `internal/agent/xray_xhttp_test.go`.

Progress:

- [x] Reproduced the failure with the SBP-pinned Xray 26.3.27 image, an ordinary
  Docker bridge, the exact SBP security profile, and the `443:8443` port map.
  The same key, UUID, client, and container configuration passed traffic with
  `www.googletagmanager.com` and failed when only the REALITY target and client
  SNI changed to `www.cloudflare.com`.
- [x] Replaced the generated target, persisted SNI, metadata fallback, and
  allowed server name with `www.googletagmanager.com`. Added regression tests
  for the TCP `dest`, XHTTP `target`, allowed server name, and default link SNI.
- [x] Formatting, the complete Go test suite, shell lifecycle assertions, vet,
  JavaScript syntax checks, and diff checks pass.
- [ ] Validate a fresh SBP-generated profile on the affected server.

Validation commands:

```bash
test -z "$(gofmt -l .)"
go test ./...
bash deploy/test_scripts.sh
go vet ./...
git diff --check
```

Recovery path: retain the last stable release and the working official Amnezia
installation until a fresh SBP profile passes. If validation fails, remove only
the exact SBP-owned test component and restore the official container; do not
adopt or mutate Amnezia-owned state.

### Align SBP AmneziaWG with the working official Self-hosted installation

Desired outcome: AmneziaWG installed by SBP works through the AmneziaVPN client
on the same fresh Ubuntu 24.04 server where the official Amnezia Self-hosted
installation is known to work.

Constraints and acceptance criteria:

- Preserve SBP ownership, isolation, bounded logging, cleanup, and per-device
  runtime update invariants.
- Do not copy proprietary material or guess undocumented parameters. Use the
  current open-source Amnezia implementation and observed non-secret runtime
  state as references.
- Do not expose or commit private keys, preshared keys, passwords, client
  configurations, or production addresses.
- A fresh SBP installation must establish an AmneziaWG handshake and pass DNS,
  small-request, and sustained-download tests through an AmneziaVPN client.
- Existing Xray behavior remains unchanged unless a separately proven shared
  cause requires a minimal correction.

Implementation context:

- SBP lifecycle and generated configuration are in `internal/agent`, primarily
  `agent.go`, `amnezia.go`, and their tests.
- The official comparison target is the current AmneziaVPN Self-hosted server
  installation and its public upstream source.
- Compare image/version, userspace or kernel implementation, container network
  mode, capabilities, sysctls, interface MTU, address/routes, NAT/FORWARD rules,
  DNS, port selection, and client parameters.

Progress:

- [x] Reproduced the distinguishing observation: official Self-hosted
  AmneziaWG works on the affected VPS, while the SBP-installed variant does not.
- [x] Captured a redacted inventory of the working official installation. It
  uses AWG 2.0 parameters with Jmin 10, Jmax 50, S1 through S4, and four ordered
  non-overlapping H ranges. Its MTU is 1420 and the active forwarding and
  masquerade path is otherwise materially equivalent to SBP.
- [x] Compared the generator with the open-source Amnezia implementation. The
  stable working installation uses AWG 2.0, while the current development
  branch has advanced to AWG 3.1. SBP will align with the observed stable AWG
  2.0 format instead of introducing the newer format before its client import
  path and interoperability are established. The official AWG 2.0 client
  profile also carries its public DNS-shaped I1 template, while the server
  keeps that client-only field inactive; SBP follows the same split.
- [x] Implemented AWG 2.0 parameter generation, separate server and client
  settings, client I1 inheritance, metadata propagation, and pinned runtime
  coverage without changing lifecycle or ownership boundaries.
- [x] Formatting, the complete Go test suite, shell lifecycle assertions, vet,
  JavaScript syntax checks, repeated generator tests, and diff checks pass.
  The pinned-image Docker integration test remains release-CI-only because the
  local Windows environment has no Docker daemon.
- [ ] Validate on a fresh server through AmneziaVPN and record the outcome.

Validation commands:

```bash
test -z "$(gofmt -l .)"
go test ./...
bash deploy/test_scripts.sh
go vet ./...
node --check internal/panel/web/app.js
node --check internal/panel/web/check.js
git diff --check
```

Recovery path: retain the last stable release and its pinned images. Any live
diagnostic change must be explicitly reversible. If the new implementation
fails validation, remove only the exact SBP-owned test component and reinstall
the stable release; never mutate the working official Amnezia installation.

## ExecPlan format

For multi-step, architectural, destructive, security-sensitive, or long-running
work, replace the line above with a plan that includes:

- the desired observable outcome;
- constraints and acceptance criteria;
- exact implementation context;
- progress, decisions, and discoveries kept current while work proceeds;
- validation commands and their results;
- a recovery path for partial failure;
- the final outcome and any remaining risks.

Remove a completed plan after its outcome is recorded and the work is fully
verified. Do not use this file as a historical changelog.
