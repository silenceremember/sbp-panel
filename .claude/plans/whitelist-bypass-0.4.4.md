# whitelist-bypass 0.4.4 upgrade

## Outcome

Routing components (WB Stream, Yandex Telemost, DION, VK Calls) run
whitelist-bypass 0.4.4. Servers with the SBP-built 0.3.8 image show Update,
which rebuilds the image and moves existing room containers to it without
changing room links. Room creation failures show the creator's own log. DION
rooms keep working after token rotation.

## Why

0.3.8 is obsolete against current provider APIs: 0.4.2 fixed WB token
handling and a VK topology panic, 0.4.3 Telemost config parsing, 0.4.4
switched WB to an exported `wb_access_token` cookie. DION rewrites its cookie
file after every refresh (upstream docs/SETUP.md) and its refresh token is
single-use, which failed on the shared read-only mount.

## Implementation (internal/agent/agent.go unless noted)

- Pin: `bypassVersion`, `wbReleaseURL`, `wbReleaseSHA256`
  (`4d4b748e...73f7`, verified). The archive is 54 MB, so the download limit
  is 64 MiB. Binary names and every flag SBP passes are unchanged; 0.4.x only
  adds `--allow-private-dst` / `--allow-loopback`, both off and not passed.
- `bypassLegacyVersions` (`0.3.8`): owned + legacy image => installed,
  `CanUpdate`; unowned => external. Uninstall removes all tags.
- Update reuses the install job: `installWhitelistBypass` calls
  `updateWhitelistBypass` when an owned legacy image exists. It builds the new
  image in `image-build/` (always removed, also at agent start), recreates
  existing room containers, restarts a failed room on the legacy image, and
  removes legacy images only after every room moved.
- `bypassRunImage`: rooms use the pinned image, or the legacy one until Update.
  All bypass `docker run` calls use `--pull never`.
- Init container: `json-file` log capped at 1 MiB, removed with the container;
  the wait stops as soon as it exits.
- DION: `bypassDeviceCookies` gives each device room a private copy in its
  device directory mounted at `/data`. A new upload replaces copies room by
  room after the container stops; clearing credentials deletes them.
- Panel (`internal/panel/panel.go`): `upgrade` is offered before room drift;
  matching rooms with an old revision offer `routing`, which only records the
  revision. UI (`app.js`): bypass Update runs the install job; DION notice.

## Validation

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
bash deploy/test_scripts.sh
node --check internal/panel/web/app.js
git diff --check
```

## Recovery

A failed Update keeps the legacy image and Update stays offered; retry skips
rooms already on the new image. Removing the component removes all bypass
containers, both image tags, the managed directory, and the cookies.

## Progress

- [x] Upstream diff, flags, archive, checksum verified.
- [x] CLAUDE.md replaces AGENTS.md and PLANS.md.
- [x] Agent, panel, UI changes; tests added.
- [x] `node --check internal/panel/web/app.js`.
- [ ] Go checks (no Go toolchain on the authoring machine).
- [ ] Live server: Update WB/Telemost/VK/DION, recreate profiles.

## Remaining risks

- A room whose container was removed but not recreated because the agent
  crashed mid-update is not restarted by retry; toggling the device restores it.
- Uploading cookies restarts every saved room, including disabled devices
  (pre-existing behavior).
- With several DION devices and no email/password in the file, only the first
  room keeps a valid refresh chain.
