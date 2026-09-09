# Changelog

Concise user-facing changes for every SBP release. Pre-release status is shown
on GitHub and is not repeated here.

## [1.7.1] - 2026-09-09

- Browser tabs now show SBP and the server country, updating immediately when the country is changed.
- Group code exports use filenames such as Ireland_Family_Group.md, with the server country followed by the group name.

## [1.7.0] - 2026-09-09

- Replaced common edit, remove, copy, settings, refresh and sign-out buttons with compact SVG icons and clear tooltips. Install, Update and confirmation actions keep their text.
- Added Export codes for each group: download a Markdown file with connection names, codes and the group's check link at the end.
- Unified action styling across groups, devices and notifications, with larger mobile targets and less empty space in component tables. Icons are bundled locally without external dependencies.

## [1.6.2] - 2026-09-09

- Fixed background scrolling through dialogs and unified spacing, file uploads and action buttons. Provider cookies now apply with Save; Cancel leaves them unchanged.
- Made mobile component tables horizontally scrollable with readable Details and stopped the header from following page scrolling.
- Removed unused controls and duplicate styles, and fixed disabled buttons leaking between dialogs.

## [1.6.1] - 2026-09-09

- Saving AmneziaWG, Xray or XHTTP settings now refreshes profiles automatically without deleting devices. Import changed profiles in your apps.
- Replaced AWG auto values with explicit defaults; restoring defaults preserves the header protection key.
- Added editable names such as Ireland - Admin - Amnezia, with numbered duplicates and automatic country detection.

## [1.6.0] - 2026-09-09

- Added Amnezia-compatible Xray/XHTTP QR codes, editable REALITY settings and per-device fingerprints.
- Added configuration export and replacement restore for groups, dates, profiles and component settings. Restore issues new connection profiles.
- Improved mobile device cards and dialogs.

## [1.5.2] - 2026-09-09

- Updated AmneziaWG to 3.1.20260828 with compatible header protection, padding and MTU defaults.
- Fixed AmneziaWG component updates and rollback while keeping device records. After updating SBP, update the AmneziaWG component and import the new profiles.

## [1.5.1] - 2026-08-29

- Fixed AmneziaWG QR import by encoding the native profile expected by its client while keeping the existing `vpn://` clipboard format.
- Made credential QR codes larger and sharper for more reliable scanning.

## [1.5.0] - 2026-08-29

- Replaced metadata-only Xray updates with an atomic global refresh of every TCP or XHTTP link while preserving UUIDs and the running containers.
- Added a forward-retryable global Update that reconciles Whitelist Bypass rooms to the current one-room-per-device layout for traffic attribution.

## [1.4.4] - 2026-08-29

- Added global profile-revision detection so component Update appears after generated configuration changes.
- Added an atomic AmneziaWG profile refresh that applies MTU 1280 to every existing AWG 3.1 profile without changing server keys, peers, or the container.

## [1.4.3] - 2026-08-29

- Changed newly issued AmneziaWG client profiles to use the conservative MTU 1280 default for more reliable Windows and mobile connectivity.

## [1.4.2] - 2026-08-25

- Added per-device traffic attribution for Whitelist Bypass rooms and group totals.
- Removed the redundant provider traffic row from group cards.

## [1.4.1] - 2026-08-25

- Added one component-level Update action that marks all matching profiles with the current protocol version.
- Added Windows and Android links for clients available on both platforms.

## [1.4.0] - 2026-08-25

- Previewed AmneziaWG protocol 3.1 with conservative server defaults and AmneziaVPN 5.0.1.5.
- Added an atomic AmneziaWG deployment update that rotates server and device keys, rebuilds the container, and republishes every profile with rollback on failure.

## [1.3.3] - 2026-08-25

- Added per-device traffic attribution for Whitelist Bypass rooms and group totals.
- Removed the redundant provider traffic row from group cards.

## [1.3.2] - 2026-08-25

- Added one component-level Update action that marks all matching profiles with the current protocol version without rewriting profiles, keys, rooms, or containers.
- Added Windows and Android links for clients available on both platforms.

## [1.3.1] - 2026-08-25

- Removed per-device profile updates and automatic migration of historical shared routing rooms.
- Kept the stable AmneziaWG 2.0 profile and AmneziaVPN 4.8.21.0 combination.

## [1.3.0] - 2026-08-25

- Added protocol versions to device profiles and introduced profile refresh support.
- Changed new Whitelist Bypass devices to use one independent room per device.

## [1.2.0] - 2026-08-24

- Added Docker Compose v2 install, repair, and removal to Docker settings with ownership checks and rollback.
- Moved confirmed external-component removal into actionable notifications and the shared lifecycle lock.

## [1.1.4] - 2026-08-24

- Pinned the recommended Windows Xray client to the validated v2rayN 7.20.4 release.

## [1.1.3] - 2026-08-24

- Kept notifications persistent above dialogs instead of recreating them on every open.
- Stabilized dialog stacking and background scrolling without shifting the page.

## [1.1.2] - 2026-08-24

- Simplified Xray settings into one atomic form for the REALITY target and complete additional-SNI list.
- Removed numeric input arrows and fixed dialog Save and Cancel behavior.

## [1.1.1] - 2026-08-24

- Split the REALITY target into hostname and port fields and allowed any validated target port.
- Loaded settings before opening dialogs and kept notifications above them.

## [1.1.0] - 2026-08-24

- Added component Settings that remain available before and after installation.
- Added editable Xray REALITY targets and SNI values, AmneziaWG server parameters, and network tuning with safe reapply and defaults.
- Added a read-only Docker container inventory.

## [1.0.3] - 2026-08-23

- Generated AmneziaWG 2.0 profiles compatible with the official AmneziaVPN client.
- Replaced the default Xray REALITY target with a validated working endpoint.

## [1.0.2] - 2026-08-22

- Added provenance checks for safely removing confirmed external network tuning settings.

## [1.0.1] - 2026-08-22

- Added explicit stable and pre-release installation channels.
- Distinguished external components and added confirmed removal for exact supported prerequisites.

## [1.0.0] - 2026-08-22

- Released the first SBP 1.x baseline for a fresh Ubuntu 24.04 server.
- Added Xray TCP, Xray XHTTP, AmneziaWG 2.0, and Whitelist Bypass management from one panel.
- Added groups, devices, expiration, current-month traffic, an isolated privileged agent, and health-checked updates with rollback.
