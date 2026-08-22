#!/usr/bin/env bash
set -Eeuo pipefail

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  SBP_ACCENT=$'\033[38;2;239;155;71m'
  SBP_SUCCESS=$'\033[38;2;72;211;143m'
  SBP_DANGER=$'\033[38;2;255;107;129m'
  SBP_BOLD=$'\033[1m'
  SBP_RESET=$'\033[0m'
else
  SBP_ACCENT=''
  SBP_SUCCESS=''
  SBP_DANGER=''
  SBP_BOLD=''
  SBP_RESET=''
fi

if [ "$(id -u)" -ne 0 ]; then
  printf '%sRun as root: sudo sbp-panel-uninstall%s\n' "${SBP_DANGER}" "${SBP_RESET}" >&2
  exit 1
fi

UFW_RULE_MARKER=/etc/vpn-panel/ufw-9443-owned
UFW_RULE_SPEC="ufw allow 9443/tcp comment 'Simple Bridge Panel'"

remove_owned_ufw_rule() {
  if [ ! -f "${UFW_RULE_MARKER}" ] || [ "$(cat "${UFW_RULE_MARKER}")" != "${UFW_RULE_SPEC}" ]; then
    return
  fi
  if ! command -v ufw >/dev/null; then
    return
  fi
  remaining="$(ufw show added 2>/dev/null | grep -Fxc "${UFW_RULE_SPEC}" || true)"
  while [ "${remaining}" -gt 0 ]; do
    if ! ufw --force delete allow 9443/tcp comment 'Simple Bridge Panel' >/dev/null 2>&1; then
      break
    fi
    remaining=$((remaining - 1))
  done
}

printf '%s%sRemove SBP Panel?%s\n' "${SBP_DANGER}" "${SBP_BOLD}" "${SBP_RESET}"
cat <<'EOF'

This removes only the panel itself:
  - the web panel and privileged agent services;
  - panel binaries and runtime files;
  - the panel database, sessions, TLS files and uploaded service cookies;
  - the dedicated sbp-panel-uninstall command and vpn-panel system account.

Docker, network tuning, Xray, AmneziaWG, routing containers, their images and
/opt/vpn-panel-managed are NOT changed. Remove managed components from the
dashboard first if you do not want them left on this server.
EOF

printf '\n%sType yes to continue [yes/no]:%s ' "${SBP_ACCENT}" "${SBP_RESET}"
read -r answer
if [ "${answer}" != "yes" ]; then
  printf '%sCancelled.%s Nothing was changed.\n' "${SBP_ACCENT}" "${SBP_RESET}"
  exit 0
fi

systemctl stop sbp-panel-update-watchdog.timer 2>/dev/null || true
systemctl stop sbp-panel-update-watchdog.service 2>/dev/null || true
systemctl disable --now vpn-panel.service 2>/dev/null || true
systemctl disable --now vpn-panel-agent.service 2>/dev/null || true
rm -f \
  /etc/systemd/system/vpn-panel.service \
  /etc/systemd/system/vpn-panel-agent.service \
  /etc/systemd/system/multi-user.target.wants/vpn-panel.service \
  /etc/systemd/system/multi-user.target.wants/vpn-panel-agent.service
systemctl daemon-reload
systemctl reset-failed \
  vpn-panel.service \
  vpn-panel-agent.service \
  sbp-panel-update-watchdog.service \
  sbp-panel-update-watchdog.timer \
  2>/dev/null || true

remove_owned_ufw_rule

rm -rf \
  /opt/vpn-panel \
  /etc/vpn-panel \
  /var/lib/vpn-panel \
  /var/lib/vpn-panel-agent \
  /run/vpn-panel \
  /usr/share/licenses/sbp-panel

if getent passwd vpn-panel >/dev/null; then
  userdel vpn-panel
fi
if getent group vpn-panel >/dev/null; then
  groupdel vpn-panel
fi

rm -f \
  /usr/local/sbin/sbp-panel-uninstall \
  /usr/local/sbin/sbp-panel-uninstall.next \
  /usr/local/sbin/sbp-panel-update \
  /usr/local/sbin/sbp-panel-update.next

printf '%sSBP Panel was removed.%s Managed components and network settings were left untouched.\n' "${SBP_SUCCESS}" "${SBP_RESET}"
