<p align="center">
  <img src="docs/sbp-simple-bridge-panel.svg" alt="SBP - Simple Bridge Panel" width="560">
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

Got a server? Good. SBP helps you manage Xray, AmneziaWG and whitelist-bypass services so your friends can actually connect to it.

## What does it do?

- **One dashboard:** components, server health, devices and monthly traffic. Also fits on your phone, because servers tend to need attention when you are nowhere near a desk.
- **Groups and access:** give friends or a team their own devices, set an expiration date or leave access unlimited. Toggle individual devices when needed.
- **Profiles to go:** copy credentials, show a QR code, or download a group's codes and check link in one file. Send it over.
- **Moving servers?** Export your groups and settings, restore them on another SBP server, and share the newly generated profiles.
- **Updates with a button:** update SBP and manage components from the panel. The terminal has earned a break.

## Panel preview

A rather tall screenshot. Scroll responsibly.

<details>
<summary>Open the full dashboard screenshot</summary>

The preview is from an earlier release; some controls may look different today.

<p align="center">
  <a href="docs/panel-preview.png">
    <img src="docs/panel-preview.png" alt="Simple Bridge Panel dashboard" width="900">
  </a>
</p>

</details>

## Supported components

| Component | What you get |
|---|---|
| [Xray](https://github.com/XTLS/Xray-core) | VLESS over TCP with REALITY and XTLS Vision |
| Xray XHTTP | VLESS over XHTTP with REALITY, managed separately from TCP |
| [AmneziaWG](https://github.com/amnezia-vpn/amneziawg-go) | AmneziaWG 3.1 server and client profiles |
| [Whitelist Bypass](https://github.com/kulikov0/whitelist-bypass) | WB Stream, Yandex Telemost, DION and VK Calls |
| Docker and network tuning | Install and manage the supporting server components |

## Requirements

| | Minimum | Recommended |
|---|---|---|
| CPU | 1 vCPU | 2 vCPU |
| RAM | 1 GB | 2 GB |
| Storage | 10 GB SSD | 20 GB SSD |

Use a **fresh Ubuntu 24.04 LTS server (amd64)** with a directly reachable public IPv4 address and root or sudo access. Existing VPN installations are not adopted by SBP.

Allow **9443/TCP** for the panel and the ports for your chosen components: **443/TCP** for Xray, **28443/TCP** for XHTTP and **48692/UDP** for AmneziaWG.

## Install in one command

Connect to your server over SSH and run:

```bash
curl -fsSL https://raw.githubusercontent.com/silenceremember/sbp-panel/main/install.sh | sudo bash
```

Open `https://YOUR_SERVER_IP:9443`. Sign in as `admin` with the password you set during installation. The default self-signed certificate causes a browser warning.

### First connection

1. Open **Components**, review settings, and install the components you need. Routing integrations require their provider cookies.
2. Create a group and add a device.
3. Copy its profile or scan its QR code in a compatible client. For AmneziaVPN's Xray import, choose **Amnezia QR**.

| Profiles | Clients |
|---|---|
| Xray TCP and XHTTP | [v2rayN](https://github.com/2dust/v2rayN) for Windows, [v2rayNG](https://github.com/2dust/v2rayNG) for Android |
| AmneziaWG | [AmneziaVPN](https://github.com/amnezia-vpn/amnezia-client) |
| Routing integrations | [Whitelist Bypass](https://github.com/kulikov0/whitelist-bypass) |

## Update and uninstall

Use **Check for updates** in the panel or run:

```bash
sudo sbp-panel-update
```

Stable releases are the default; enable **Pre-release** only to try test builds. See [release notes](CHANGELOG.md).

To uninstall:

```bash
sudo sbp-panel-uninstall
```

Managed components keep running after panel removal. Remove them through the dashboard first if you want to remove everything.

## Versioning

Versions use `X.Y.Z`:

| Segment | Meaning |
|---|---|
| `X` | A major release that may break compatibility |
| `Y` | A backwards-compatible feature release |
| `Z` | A compatible fix or small polish update |

## What's next?

First, keep the panel simple and fix things that actually annoy people. Then there are a few bigger ideas:

- **SBP Linker:** manage several SBP servers in one place.
- **Telegram bot:** payments, renewals and automatic access delivery.
- **SBP VPN client:** keep connections across supported protocols in one app.

Still on the drawing board. Please do not buy a server for the imaginary Telegram bot just yet.

## Contributing

Found a bug, have an idea, or see something that could be clearer? [Open an issue](https://github.com/silenceremember/sbp-panel/issues) or send a focused pull request. Your passwords and cookies are not - keep those to yourself.

Thinking about making a fork? Please consider contributing here first. One stronger project is easier for everyone to use and maintain.

## FAQ

| Question | Answer |
|---|---|
| Is "SBP Panel" technically "Simple Bridge Panel Panel"? | Yes, a bit like "PUBG: Battlegrounds." We have made peace with it. |
| What does SBP stand for? | Simple Bridge Panel. Suspiciously Big Pizza is also an acceptable answer. |
| Is SBP a VPN provider? | Nope. You bring the server; SBP helps you manage the software. |

<p align="center">
  <img src="docs/suspiciously-big-pizza.svg" alt="Suspiciously Big Pizza" width="560">
</p>

## License

[Apache License 2.0](LICENSE) - keep the license and attribution from [NOTICE](NOTICE) where required. A link back to the [original project](https://github.com/silenceremember/sbp-panel) is appreciated, too.

Integrated projects retain their own licenses and trademarks. Inclusion does not imply affiliation or endorsement.
