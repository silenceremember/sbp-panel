#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  SBP_ACCENT=$'\033[38;2;239;155;71m'
  SBP_SUCCESS=$'\033[38;2;72;211;143m'
  SBP_DANGER=$'\033[38;2;255;107;129m'
  SBP_URL=$'\033[48;2;239;155;71m\033[30;1m'
  SBP_RESET=$'\033[0m'
else
  SBP_ACCENT=''
  SBP_SUCCESS=''
  SBP_DANGER=''
  SBP_URL=''
  SBP_RESET=''
fi

if [ "${EUID}" -ne 0 ]; then
  printf '%sRun as root: sudo bash bootstrap.sh%s\n' "${SBP_DANGER}" "${SBP_RESET}" >&2
  exit 1
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PREFIX=/opt/vpn-panel
ETC=/etc/vpn-panel
STATE=/var/lib/vpn-panel
CONFIG="${ETC}/config.json"
UFW_RULE_MARKER="${ETC}/ufw-9443-owned"
UFW_RULE_SPEC="ufw allow 9443/tcp comment 'Simple Bridge Panel'"
LICENSE_DIR=/usr/share/licenses/sbp-panel
BIN_SOURCE="${SCRIPT_DIR}/sbp-panel-linux-amd64"
if [ ! -f "${BIN_SOURCE}" ]; then
  echo "Missing executable: ${BIN_SOURCE}" >&2
  exit 1
fi

LICENSE_SOURCE="${SCRIPT_DIR}/LICENSE"
NOTICE_SOURCE="${SCRIPT_DIR}/NOTICE"
if [ ! -f "${LICENSE_SOURCE}" ] || [ ! -f "${NOTICE_SOURCE}" ]; then
  echo "Missing LICENSE or NOTICE in the installation bundle" >&2
  exit 1
fi

cleanup_bootstrap_temps() {
  rm -f \
    "${PREFIX}/bin/vpn-panel.next" \
    "${CONFIG}.next" \
    "${UFW_RULE_MARKER}.tmp" \
    "${LICENSE_DIR}/LICENSE.next" \
    "${LICENSE_DIR}/NOTICE.next" \
    /usr/local/sbin/sbp-panel-uninstall.next \
    /usr/local/sbin/sbp-panel-update.next
}
trap cleanup_bootstrap_temps EXIT

install -d -m 0755 "${PREFIX}/bin" "${ETC}"
install -d -m 0755 "${LICENSE_DIR}"
install -d -m 0750 "${STATE}" /run/vpn-panel
install -d -o root -g root -m 0700 /var/lib/vpn-panel-agent /var/lib/vpn-panel-agent/secrets /var/lib/vpn-panel-agent/secrets/bypass

find /var/lib/vpn-panel-agent/secrets/bypass -type f -name 'cookies.json.tmp' -delete 2>/dev/null || true

install -m 0755 "${BIN_SOURCE}" "${PREFIX}/bin/vpn-panel.next"
mv -f "${PREFIX}/bin/vpn-panel.next" "${PREFIX}/bin/vpn-panel"
install -m 0644 "${LICENSE_SOURCE}" "${LICENSE_DIR}/LICENSE.next"
install -m 0644 "${NOTICE_SOURCE}" "${LICENSE_DIR}/NOTICE.next"
mv -f "${LICENSE_DIR}/LICENSE.next" "${LICENSE_DIR}/LICENSE"
mv -f "${LICENSE_DIR}/NOTICE.next" "${LICENSE_DIR}/NOTICE"
if [ -f "${SCRIPT_DIR}/uninstall.sh" ]; then
  install -m 0755 "${SCRIPT_DIR}/uninstall.sh" /usr/local/sbin/sbp-panel-uninstall.next
else
  echo "Missing uninstall.sh in the installation bundle" >&2
  exit 1
fi
mv -f /usr/local/sbin/sbp-panel-uninstall.next /usr/local/sbin/sbp-panel-uninstall
ln -s /opt/vpn-panel/bin/vpn-panel /usr/local/sbin/sbp-panel-update.next
mv -Tf /usr/local/sbin/sbp-panel-update.next /usr/local/sbin/sbp-panel-update
# `install -d` does not change the mode of an existing directory.
chmod 0755 "${PREFIX}" "${PREFIX}/bin" "${ETC}"

getent group vpn-panel >/dev/null || groupadd --system vpn-panel
id vpn-panel >/dev/null 2>&1 || useradd --system --gid vpn-panel --home-dir "${STATE}" --shell /usr/sbin/nologin vpn-panel
chown -R vpn-panel:vpn-panel "${STATE}"
chown root:vpn-panel /run/vpn-panel
chmod 0750 /run/vpn-panel

if [ ! -s "${ETC}/tls.key" ] || [ ! -s "${ETC}/tls.crt" ]; then
  command -v openssl >/dev/null || { apt-get update; apt-get install -y --no-install-recommends openssl ca-certificates; }
  HOST_IP="$(hostname -I | awk '{print $1}')"
  rm -f "${ETC}/tls.key" "${ETC}/tls.crt"
  openssl req -x509 -newkey rsa:3072 -nodes -days 825 \
    -keyout "${ETC}/tls.key" -out "${ETC}/tls.crt" \
    -subj "/CN=${HOST_IP}" -addext "subjectAltName=IP:${HOST_IP}"
  chmod 0640 "${ETC}/tls.key"
  chown root:vpn-panel "${ETC}/tls.key"
fi

if [ ! -e "${CONFIG}" ] && [ ! -L "${CONFIG}" ]; then
  cat > "${CONFIG}.next" <<'EOF'
{
  "listen": ":9443",
  "database": "/var/lib/vpn-panel/panel.db",
  "tls_cert": "/etc/vpn-panel/tls.crt",
  "tls_key": "/etc/vpn-panel/tls.key",
  "agent_socket": "/run/vpn-panel/agent.sock",
  "bypass_secrets_dir": "/var/lib/vpn-panel-agent/secrets/bypass",
  "cookie_secure": true
}
EOF
  chmod 0640 "${CONFIG}.next"
  chown root:vpn-panel "${CONFIG}.next"
  mv -f "${CONFIG}.next" "${CONFIG}"
elif [ ! -f "${CONFIG}" ]; then
  echo "Existing config path is not a regular file: ${CONFIG}" >&2
  exit 1
fi
chmod 0640 "${CONFIG}"
chown root:vpn-panel "${CONFIG}" "${ETC}/tls.crt" "${ETC}/tls.key"
chmod 0640 "${ETC}/tls.crt" "${ETC}/tls.key"

if ! runuser -u vpn-panel -- "${PREFIX}/bin/vpn-panel" -mode has-owner -config "${CONFIG}" >/dev/null 2>&1; then
  while true; do
    read -r -s -p "Password for admin (minimum 8 characters): " PANEL_PASSWORD
    echo
    read -r -s -p "Repeat password: " PANEL_PASSWORD_REPEAT
    echo
    if [ "${PANEL_PASSWORD}" != "${PANEL_PASSWORD_REPEAT}" ]; then
      echo "Passwords do not match."
      continue
    fi
    if [ "${#PANEL_PASSWORD}" -lt 8 ]; then
      echo "Password must contain at least 8 characters."
      continue
    fi
    printf '%s\n' "${PANEL_PASSWORD}" | runuser -u vpn-panel -- "${PREFIX}/bin/vpn-panel" -mode init-owner -config "${CONFIG}"
    unset PANEL_PASSWORD PANEL_PASSWORD_REPEAT
    break
  done
fi
rm -f "${STATE}/bootstrap.token"

cat > /etc/systemd/system/vpn-panel-agent.service <<'EOF'
[Unit]
Description=Simple Bridge Panel privileged agent
After=network.target docker.service

[Service]
Type=simple
Group=vpn-panel
ExecStart=/opt/vpn-panel/bin/vpn-panel -mode agent -config /etc/vpn-panel/config.json
Restart=on-failure
RestartSec=3
StandardOutput=null
StandardError=null
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
# This is the deliberately privileged, allowlisted installer process.
# It needs to write sysctl files, install packages and create managed stacks.
ProtectSystem=false

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/vpn-panel.service <<'EOF'
[Unit]
Description=Simple Bridge Panel web interface
After=network.target vpn-panel-agent.service
Requires=vpn-panel-agent.service

[Service]
Type=simple
User=vpn-panel
Group=vpn-panel
ExecStart=/opt/vpn-panel/bin/vpn-panel -mode serve -config /etc/vpn-panel/config.json
Restart=on-failure
RestartSec=3
StandardOutput=null
StandardError=null
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/vpn-panel

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable vpn-panel-agent.service vpn-panel.service
systemctl restart vpn-panel-agent.service vpn-panel.service

if command -v ufw >/dev/null && ufw status | grep -q '^Status: active'; then
  if [ -f "${UFW_RULE_MARKER}" ] && [ "$(cat "${UFW_RULE_MARKER}")" = "${UFW_RULE_SPEC}" ]; then
    if ! ufw show added 2>/dev/null | grep -Fqx "${UFW_RULE_SPEC}"; then
      ufw allow 9443/tcp comment 'Simple Bridge Panel'
    fi
  elif ! ufw show added 2>/dev/null | grep -Fqx "${UFW_RULE_SPEC}"; then
    ufw allow 9443/tcp comment 'Simple Bridge Panel'
    if ufw show added 2>/dev/null | grep -Fqx "${UFW_RULE_SPEC}"; then
      printf '%s\n' "${UFW_RULE_SPEC}" > "${UFW_RULE_MARKER}.tmp"
      chmod 0600 "${UFW_RULE_MARKER}.tmp"
      chown root:root "${UFW_RULE_MARKER}.tmp"
      mv -f "${UFW_RULE_MARKER}.tmp" "${UFW_RULE_MARKER}"
    fi
  fi
fi

PANEL_LISTEN="$(grep -oE '"listen"[[:space:]]*:[[:space:]]*"[^"]+"' "${CONFIG}" | head -n 1 | sed -E 's/^.*"([^"]+)"$/\1/' || true)"
PANEL_LISTEN="${PANEL_LISTEN:-:9443}"
PANEL_PORT="${PANEL_LISTEN##*:}"
case "${PANEL_PORT}" in
  ''|*[!0-9]*) PANEL_PORT=9443 ;;
esac
PANEL_HOST="${PANEL_LISTEN%:*}"
PANEL_HOST="${PANEL_HOST#[}"
PANEL_HOST="${PANEL_HOST%]}"
PANEL_BIND_HOST="${PANEL_HOST}"
case "${PANEL_HOST}" in
  ''|0.0.0.0) PANEL_HOST=127.0.0.1 ;;
  ::) PANEL_HOST=::1 ;;
esac
if [[ "${PANEL_HOST}" == *:* ]]; then
  PANEL_URL_HOST="[${PANEL_HOST}]"
else
  PANEL_URL_HOST="${PANEL_HOST}"
fi

PANEL_HEALTHY=0
for _ in {1..30}; do
  if systemctl is-active --quiet vpn-panel-agent.service \
    && systemctl is-active --quiet vpn-panel.service \
    && [ -S /run/vpn-panel/agent.sock ]; then
    if command -v curl >/dev/null; then
      if curl --noproxy '*' -ksSf --max-time 2 "https://${PANEL_URL_HOST}:${PANEL_PORT}/api/bootstrap/status" >/dev/null; then
        PANEL_HEALTHY=1
        break
      fi
    elif command -v ss >/dev/null && ss -ltn | grep -Eq "[:.]${PANEL_PORT}[[:space:]]"; then
      PANEL_HEALTHY=1
      break
    fi
  fi
  sleep 1
done
if [ "${PANEL_HEALTHY}" -ne 1 ]; then
  echo "SBP Panel did not become healthy within 30 seconds." >&2
  systemctl status vpn-panel-agent.service vpn-panel.service --no-pager -l >&2 || true
  exit 1
fi

SERVER_IP="$(hostname -I | awk '{print $1}')"
SERVER_IP="${SERVER_IP:-127.0.0.1}"
case "${PANEL_BIND_HOST}" in
  ''|0.0.0.0|::) PANEL_DISPLAY_HOST="${SERVER_IP}" ;;
  *) PANEL_DISPLAY_HOST="${PANEL_BIND_HOST}" ;;
esac
if [[ "${PANEL_DISPLAY_HOST}" == *:* ]]; then
  PANEL_DISPLAY_HOST="[${PANEL_DISPLAY_HOST}]"
fi
echo
PANEL_URL="https://${PANEL_DISPLAY_HOST}:${PANEL_PORT}"
if [ -n "${SBP_URL}" ]; then
  printf '%s  Simple Bridge Panel: %s  %s\n' "${SBP_URL}" "${PANEL_URL}" "${SBP_RESET}"
else
  echo "Simple Bridge Panel: ${PANEL_URL}"
fi
printf '%sReady.%s\n' "${SBP_SUCCESS}" "${SBP_RESET}"
printf '%sUpdate:%s sudo sbp-panel-update\n' "${SBP_ACCENT}" "${SBP_RESET}"
printf '%sUninstall:%s sudo sbp-panel-uninstall\n' "${SBP_ACCENT}" "${SBP_RESET}"
echo "Login: admin"
echo "Discovery is read-only. Existing VPN containers were not restarted or changed."
