<p align="center">
  <img src="docs/sbp-simple-bridge-panel.svg" alt="SBP - Simple Bridge Panel" width="560">
</p>

<p align="center">
  <strong>Your server. Your software. One panel.</strong>
</p>

<p align="center">
  <a href="https://github.com/silenceremember/sbp-panel/releases/latest"><img src="https://img.shields.io/github/v/release/silenceremember/sbp-panel?display_name=tag&amp;sort=semver" alt="Latest release"></a>
  <a href="https://github.com/silenceremember/sbp-panel/actions/workflows/release.yml"><img src="https://github.com/silenceremember/sbp-panel/actions/workflows/release.yml/badge.svg" alt="Build status"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-EF9B47.svg" alt="Apache-2.0 license"></a>
  <img src="https://img.shields.io/badge/platform-Ubuntu%2024.04%20LTS-EF9B47.svg" alt="Ubuntu 24.04 LTS">
</p>

<p align="center">
  <a href="https://boosty.to/silenceremember"><img src="docs/support-boosty.png" alt="Support SBP on Boosty" height="38"></a>
  <a href="https://t.me/someshitnobodyaskedfor"><img src="docs/follow-telegram.png" alt="Follow SBP on Telegram" height="38"></a>
  <a href="https://discord.gg/6k8W8e7p8Z"><img src="docs/community-discord.png" alt="Join the SBP Discord community" height="38"></a>
</p>

# Simple Bridge Panel

SBP is a small self-hosted panel for preparing and managing a fresh Ubuntu VPN server. It installs supported components, creates client profiles, manages access and expiration, tracks current-month traffic, and performs health-checked updates from one dashboard.

SBP is not a VPN provider or hosted service. It manages third-party software on **your own server**.

## What it manages

- Server components and their lifecycle.
- Groups, expiration, devices, credentials, and QR profiles.
- Xray TCP, Xray XHTTP, and AmneziaWG access.
- WB Stream, Yandex Telemost, DION, and VK Calls routing rooms.
- CPU, memory, disk, uptime, network, and monthly traffic estimates.

## Panel preview

Groups, devices, traffic, server health, components, and credentials all live on one dashboard. The screenshot is tall because the panel keeps everything in one place.

<details>
<summary><strong>Open the full panel screenshot</strong></summary>

This example is from an earlier release, so some labels and versions may differ from the current build.

<p align="center">
  <a href="docs/panel-preview.png">
    <img src="docs/panel-preview.png" alt="Simple Bridge Panel dashboard preview" width="900">
  </a>
</p>

</details>

## Components

| Component | Version | Purpose |
|---|---:|---|
| Network tuning | - | Validated TCP congestion control and queue discipline settings |
| Docker | Ubuntu package | Isolated managed services |
| [Xray](https://github.com/XTLS/Xray-core) | 26.3.27 | VLESS over TCP with REALITY and XTLS Vision |
| [Xray](https://github.com/XTLS/Xray-core) XHTTP | 26.3.27 | VLESS over XHTTP with REALITY |
| [AmneziaWG](https://github.com/amnezia-vpn/amneziawg-go) | Protocol 3.1; engine 3.1.20260814 | AmneziaWG server and device profiles with conservative obfuscation defaults |
| [Whitelist Bypass](https://github.com/kulikov0/whitelist-bypass) | 0.3.8 | WB Stream, Telemost, DION, and VK Calls |

SBP provides a management layer around these upstream projects; it does not create or own their protocols.

## Requirements

| Item | Requirement |
|---|---|
| Server | Linux amd64; Ubuntu 24.04 LTS is recommended and tested. Other compatible distributions may work but are not officially supported |
| Access | Root or sudo over SSH |
| Network | Directly reachable public IPv4 address |
| Minimum | 1 vCPU, 1 GB RAM, 10 GB SSD |
| Recommended | 2 vCPU, 2 GB RAM, 20 GB SSD for all components or several users |

| Port | Protocol | Used by |
|---:|---|---|
| 9443 | TCP | SBP panel |
| 443 | TCP | Xray |
| 28443 | TCP | Xray XHTTP |
| 48692 | UDP | AmneziaWG |

A clean server is recommended. Existing VPN software, containers, occupied ports, and custom networking are treated as external and are not adopted by SBP.

## Install in one command

Connect to the server over SSH and run:

```bash
curl -fsSL https://raw.githubusercontent.com/silenceremember/sbp-panel/main/install.sh | sudo bash
```

Then open:

```text
https://YOUR_SERVER_IP:9443
```

| Login detail | Value |
|---|---|
| Username | `admin` |
| Password | The password entered during installation |
| Certificate | Self-signed by default; the browser shows a warning on the first visit |

## First steps

1. Open **Components** and review **Settings** for the components you plan to use. Settings are global desired server configuration and remain available before and after installation.
2. Install Network tuning, Docker, and the VPN methods you need. Upload the authorized cookie JSON from **Settings** for each required routing component.
3. Create a group and choose an expiration date or unlimited access.
4. Add a device and select its connection method.
5. Copy the profile or scan its QR code and import it into a client.

Profiles use the readable name `SBP · Group name · Device name`. Each profile also records its protocol version. **Edit** changes the device name. Recreate a device when it needs a newly issued profile.

Routing integrations create one independent room for each new device. Historical shared-room devices are not migrated automatically; remove and recreate them when independent rooms are required.

| Profile type | Suggested client |
|---|---|
| Xray TCP or XHTTP | [v2rayN 7.20.4](https://github.com/2dust/v2rayN/releases/tag/7.20.4) for Windows; [v2rayNG](https://github.com/2dust/v2rayNG) for Android |
| AmneziaWG | [AmneziaVPN 5.0.1.5](https://github.com/amnezia-vpn/amnezia-client/releases/tag/5.0.1.5) |
| Routing integrations | [Whitelist Bypass](https://github.com/kulikov0/whitelist-bypass) |

## Operations

| Action | Command or UI | Result |
|---|---|---|
| Update SBP | `sudo sbp-panel-update` or **Check for updates** | Checks stable releases by default, verifies size and SHA-256, checks health, and rolls back on failure |
| Try prereleases | Enable **Pre-release** beside **Check for updates** | Immediately scans stable and GitHub prerelease builds; the command-line updater stays stable-only |
| Remove the panel | `sudo sbp-panel-uninstall` | Removes SBP and its panel data, but leaves managed components running |
| Return to a clean server | Remove components in the dashboard, then uninstall SBP | Removes panel-owned components in safe dependency order before panel removal |

## Operational behavior

| Area | Behavior |
|---|---|
| Device changes | Edit changes the name; recreate a device to issue a new profile; runtime membership changes live without restarting shared containers |
| Component isolation | Xray TCP and XHTTP use separate containers, ports, configs, and traffic namespaces |
| Component lifecycle | An active install, update, or removal is restored after a browser reload and conflicting actions remain disabled until it finishes |
| AmneziaWG component update | **Update** replaces the complete managed deployment with protocol 3.1, rotates the server identity and every device key together, and requires every user to import the newly issued profile |
| Network tuning settings | The allowlisted `modprobe` and `sysctl` payload is editable before or after installation; missing lines return to validated defaults and an installed component is reapplied with rollback on failure |
| Docker settings | Install, repair, or remove Docker Compose v2; verified external Ubuntu packages can be removed without adoption, while unknown CLI plugins remain untouched; includes a read-only container list |
| REALITY settings | Each Xray component keeps its default profile SNI and can save a validated TLS target plus additional server-side SNI hostnames before installation or apply them to its installed managed container |
| AmneziaWG settings | Server-side AWG obfuscation parameters are available before and after installation; profile-affecting changes are refused while peers exist |
| Routing settings | Each routing component keeps its own cookie JSON upload, clear action, and saved rooms under **Components > Settings** |
| Expiration | Expired groups are reconciled with runtime credentials and routing access |
| Routing | One room and bounded managed container per device; traffic remains aggregated by group and provider |
| Traffic | Current UTC month only; operational estimates, not billing records |
| Persistent state | No historical traffic archive, automatic backups, editable source tree, or build toolchain |
| Logs | Persistent managed Docker logging is disabled; SBP services do not write persistent application logs |
| Administration | One fixed administrator account, no password recovery flow; administrator access is root-capable |

<p align="center">
  <img src="docs/suspiciously-big-pizza.svg" alt="Suspiciously Big Pizza" width="560">
</p>

## FAQ

| Question | Answer |
|---|---|
| Is "SBP Panel" technically "Simple Bridge Panel Panel"? | Yes, a bit like "PUBG: Battlegrounds." The product is Simple Bridge Panel; `sbp-panel` is still the repository and command name. |
| What does SBP stand for? | Simple Bridge Panel. Suspiciously Big Pizza is also an acceptable answer. |
| Is SBP a VPN provider? | Nope. It helps you manage supported software on your own server. |
| What remains after panel uninstall? | Managed components keep running on purpose. Remove them in the dashboard first if you want a clean server. |

## Contributing

Found a bug, have an idea, or see something that could be clearer? Issues, diagnostics, documentation fixes, tests, and focused pull requests are all welcome.

Thinking about making a fork? Please consider contributing here first. One stronger project is easier for everyone to use and maintain.

Never submit real server addresses, cookies, private keys, client credentials, databases, or session data.

SBP uses Go 1.26.7:

```bash
gofmt -w .
go test ./...
bash deploy/test_scripts.sh
```

## Versioning

Versions use `X.Y.Z`:

| Segment | Meaning |
|---|---|
| `X` | A major release that may break compatibility |
| `Y` | A backwards-compatible feature release |
| `Z` | A compatible fix or small polish update |

**Pre-release builds are strongly discouraged for normal use.** They remain in the release history only for developer testing, investigation, and convenient downloads, and may contain bugs or incomplete work.

The `1.4.0` prerelease observes the new AmneziaWG 3.1 and AmneziaVPN 5.0.1.5 combination. Its default preset enables header protection, keeps narrow non-overlapping header ranges, leaves random trailers and cookie suppression disabled, and does not enable optional timing or content-padding experiments. Upstream client regressions are still being tracked in [issue #3043](https://github.com/amnezia-vpn/amnezia-client/issues/3043) and [issue #3048](https://github.com/amnezia-vpn/amnezia-client/issues/3048).

## Plans

| Priority | What it means |
|---|---|
| Everyday use | Keep setup, updates, and routine management as simple as possible |
| Reliability | Fix real problems, improve recovery, and keep existing installations predictable |
| Compatibility | Maintain the supported components and Ubuntu 24.04 target without vague promises |
| New ideas | Add larger features only when people have a practical need for them |

### Planned projects

| Project | Idea |
|---|---|
| SBP Linker | Bring several SBP servers together in one place and make larger deployments easier to manage |
| Telegram bot | Handle payments, renewals, and automatic access delivery |
| SBP VPN client | Keep connections produced by SBP across supported protocols together in one simple app |

## Third-party software

SBP integrates with [Xray-core](https://github.com/XTLS/Xray-core), [AmneziaWG](https://github.com/amnezia-vpn/amneziawg-go), [AmneziaVPN](https://github.com/amnezia-vpn/amnezia-client), [Whitelist Bypass](https://github.com/kulikov0/whitelist-bypass), [v2rayN](https://github.com/2dust/v2rayN), and [v2rayNG](https://github.com/2dust/v2rayNG).

Their names, trademarks, software, and services belong to their respective owners and remain governed by their own licenses and terms. Inclusion does not imply affiliation or endorsement.

## License

Decided to fork SBP or use some of its code elsewhere? Please include a link to the [original project](https://github.com/silenceremember/sbp-panel). Other than that, it is the standard [Apache License 2.0](LICENSE) - keep the license and attribution from [NOTICE](NOTICE) where required.
