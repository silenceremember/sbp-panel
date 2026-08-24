# PLANS.md

This file contains only active execution plans that must survive across tasks.
Read `AGENTS.md`, this file, the relevant code, and `git status` before editing.
Code and tests remain the source of truth.

## Active plans

### Make component settings persistent and available before installation

Desired outcome: every component row exposes a consistent Settings action
before and after installation. Network tuning is shown without a synthetic
version and has an editable, line-oriented allowlisted configuration. AmneziaWG
has its own validated configuration. Saved values are persistent desired state,
are consumed by later installs, and are reapplied safely when the corresponding
SBP-owned component is already installed.

Constraints and acceptance criteria:

- Rename the component to `Network tuning` and omit its version in discovery,
  the dashboard, and user-facing documentation.
- Never expose an arbitrary root shell. The tuning editor may display a
  shell-like line-oriented payload, but it accepts only documented SBP-owned
  keys and validated values. Unknown commands, substitutions, redirects, and
  duplicate keys fail closed.
- Treat omitted editable lines as an explicit request for the documented
  default. Save desired settings atomically in an SBP-owned, size-bounded file
  with a defined cleanup path.
- If Network tuning is installed, Save must atomically replace the owned
  persistent payload, reapply all effective settings, verify the kernel state,
  and restore the previous payload and runtime values if application fails.
- AmneziaWG settings must validate cross-field AWG constraints. Never silently
  invalidate issued device profiles: refuse profile-affecting changes while
  peers exist, and apply a safe change only to a proven SBP-owned installation
  with staged configuration, runtime verification, and rollback.
- Component uninstall removes runtime resources but preserves desired settings
  for a later reinstall. Full product cleanup removes the settings directory;
  panel-only uninstall keeps managed state as required.
- Settings remains visible for external and uninstalled components. External
  resources are never mutated. Components without editable values show an
  explanatory read-only view rather than a dead control.
- Use the shared dialog, pending-action, notification, lifecycle lock, scroll
  preservation, and responsive action layout already present in the dashboard.
  Explain once in every Settings dialog that component settings are global
  desired configuration and remain relevant independently of install state.
- Existing Xray SNI and routing-cookie settings remain isolated by component.
  Extend Xray desired SNI storage only as needed to make the existing dialog
  usable before install. A target change must remain variant-scoped, accept
  only a DNS hostname and a port from 1 to 65535, pass a bounded TLS probe,
  staged config validation and runtime health checks, and never rewrite client
  metadata, credentials, or the other variant.

Implementation context:

- Component discovery and lifecycle operations are in
  `internal/agent/agent.go`; AWG parameter generation and runtime updates are in
  `internal/agent/amneziawg_parameters.go` and
  `internal/agent/amneziawg_runtime.go`.
- Existing Xray SNI mutation lives in `internal/agent/xray_sni.go`.
- Privileged routes are registered in `internal/agent/agent.go`; authenticated
  panel proxies are in `internal/panel/panel.go`.
- Dashboard actions and shared dialogs are in `internal/panel/web/app.js`, with
  shared styles in `internal/panel/web/app.css`.

Progress:

- [x] Inspected the current fixed tuning payload, rollback ownership metadata,
  AWG 2.0 generator, runtime sync path, component discovery, and Settings UI.
- [x] Chose validated desired-state files instead of a root-shell editor.
- [x] Implement atomic, size-bounded desired settings storage, allowlisted
  parsing, missing-line defaults, interrupted-write cleanup, and tests.
- [x] Wire desired settings into fresh installs. Installed Network tuning saves
  reapply the complete payload with file/runtime verification and rollback.
  A fresh install also refuses exact tuning paths whose SBP ownership cannot
  be proven, even when BBR is not currently active.
  Installed AmneziaWG changes stage server config and metadata, preserve
  client-only values, refuse active peers, verify runtime through the existing
  sync path, and roll back server state if metadata replacement fails.
- [x] Make Settings visible for every component before and after installation.
  Xray persists only its server-side SNI allowlist; AmneziaWG exposes only
  server J/S/H values; client fingerprint, DNS, and keepalive stay out. Routing
  components keep their cookie/room view. Docker shows a read-only container
  inventory, and remaining components without tunables show a read-only
  explanation.
- [x] Update documentation and add UI/API contract checks. Go tests, vet,
  JavaScript syntax, deploy lifecycle assertions, formatting, and diff checks
  pass locally.
- [x] Polish the Xray dialog so data renders before the modal opens, the target
  uses separate hostname and port controls, Add SNI is its own action, footer
  Save closes without validating an empty Add SNI field, the scrollbar gutter
  remains stable, and notifications stay above the modal.
- [ ] Validate both settings dialogs at normal and narrow widths in an installed
  build. The local preview server was not running during the final browser
  check, so no visual result is claimed from that attempt.

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

Recovery path: keep previous desired files, persistent component payloads, and
runtime values until the replacement passes validation and health checks. On
failure, restore the old files and runtime state and report rollback failure
explicitly. Never mutate unowned files, containers, or settings.

### Manage the REALITY target and additional SNI values for each Xray component

Desired outcome: an administrator can open Settings from an Xray TCP or XHTTP
component, inspect and safely change its REALITY target, see its allowed
REALITY SNI values, add any syntactically valid hostname, and remove only
additional values without changing the default SNI, devices, credentials, or
the other Xray variant.

Constraints and acceptance criteria:

- Keep `www.googletagmanager.com` as the immutable default profile SNI. The
  initial target remains `www.googletagmanager.com:443`, while each variant may
  persist an independently selected DNS target and TLS port.
- Store the allowlist as persistent desired component settings so it is
  available before installation. Project the same validated list into the
  selected variant's managed Xray configuration when installed; do not change
  client metadata or the immutable default profile SNI.
- Refuse mutation unless the component is SBP-owned and its container inventory,
  configuration, and metadata are valid.
- Validate hostnames, reject duplicates and wildcards, require at least the
  default SNI, and bound the list and request size.
- Test a staged configuration with the pinned Xray image before replacing the
  persistent file. Restart only the selected container, verify its API and
  runtime users, and restore the previous configuration if application fails.
- Before applying a target to an installed component, run a bounded TLS probe
  through the pinned Xray image. Run the same probe during a later installation
  when the target was saved before Docker was available.
- The Settings dialog must show the default and additional SNI values, permit
  editing the target and complete additional-SNI list in one ordinary form,
  and clearly state that profiles continue to use the default unless edited in
  the client. One Save must validate and apply the full state atomically with at
  most one installed-container restart, then close the dialog.
- Install, Remove, Settings, and unavailable component controls must have the
  same width without pinning their position.

Implementation context:

- Variant paths, managed container verification, client metadata, and Xray
  configuration helpers are in `internal/agent/xray_variants.go`,
  `internal/agent/agent.go`, and `internal/agent/xray_runtime.go`.
- The privileged API is registered in `internal/agent/agent.go`; the panel proxy
  routes are in `internal/panel/panel.go`.
- Component actions and the shared dialog are rendered in
  `internal/panel/web/app.js`; styles are in `internal/panel/web/app.css`.

Progress:

- [x] Confirmed that Xray accepts multiple server-side `serverNames`, while each
  client profile sends one SNI and each SBP Xray variant retains one shared
  target.
- [x] Implement fail-closed list, add, remove, staged validation, restart, and
  rollback behavior in the agent.
- [x] Add authenticated panel API routes and the Settings dialog.
- [x] Extend the list to persistent desired settings so the same dialog works
  before installation. Installed mutations update desired state and runtime as
  one rollback boundary; uninstall preserves desired settings for reinstall.
- [x] Add a normalized per-variant target field, a 30-second pinned-image TLS
  probe, staged application, runtime verification, rollback, and an explicit UI
  warning that imported profiles are not rewritten.
- [x] Equalize component action widths. Verified the component table at 1440
  pixels and the dialog at a narrow 500-pixel viewport.
- [x] Formatting, all Go tests, vet, both JavaScript syntax checks, shell
  lifecycle assertions, and diff checks pass. Mutation tests cover validation,
  idempotency, ownership refusal, default protection, successful removal, and
  rollback after a failed restart. The pinned-image runtime path remains
  release-CI-only because the local Windows environment has no Docker daemon.
- [x] Replace the per-target and per-SNI UI actions with one atomic form Save,
  remove the port spinner, and close editable component dialogs after a
  successful Save without hiding in-dialog error notifications. Bulk mutation
  coverage proves one validation/restart, immutable-default enforcement, and
  persistent desired-state behavior before installation. Formatting, all Go
  tests, vet, JavaScript syntax, deploy assertions, and diff checks pass.
- [ ] Verify the simplified Xray form visually at normal and narrow widths in
  a running panel build; the local preview server is not currently available.
- [ ] Validate add, reconnect, manually selected client SNI, removal, and
  unchanged operation of the other Xray variant on an installed release build.

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

Recovery path: keep the previous configuration in memory until the selected
container passes health checks. On any apply failure, atomically restore the
old configuration, restart and verify the same container, and report whether
rollback succeeded. Never mutate the other variant or any external container.

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
