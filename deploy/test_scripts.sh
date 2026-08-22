#!/usr/bin/env bash
# shellcheck disable=SC1003,SC2016 # Assertions intentionally match literal shell source.
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP="${SCRIPT_DIR}/bootstrap.sh"
UNINSTALL="${SCRIPT_DIR}/uninstall.sh"
INSTALL="${SCRIPT_DIR}/../install.sh"
RELEASE="${SCRIPT_DIR}/../.github/workflows/release.yml"
AGENT="${SCRIPT_DIR}/../internal/agent/agent.go"

fail() {
  echo "deploy script assertion failed: $*" >&2
  exit 1
}

contains() {
  local file=$1
  local text=$2
  grep -Fq -- "${text}" "${file}" || fail "${file} does not contain: ${text}"
}

bash -n "${BOOTSTRAP}"
bash -n "${UNINSTALL}"
bash -n "${INSTALL}"

contains "${INSTALL}" 'sbp-panel-update.json'
contains "${INSTALL}" 'sha256sum "${archive}"'
contains "${INSTALL}" 'actual_size="$(stat -c'
contains "${INSTALL}" 'head -c $((MAX_ARCHIVE_BYTES + 1))'
contains "${INSTALL}" 'bash "${bundle}/bootstrap.sh" </dev/tty'
contains "${INSTALL}" 'trap cleanup EXIT'
contains "${INSTALL}" 'apt-get purge -y unzip'
contains "${INSTALL}" 'Simple Bridge Panel is already installed.'
contains "${INSTALL}" 'Update:%s sudo sbp-panel-update'
contains "${INSTALL}" 'SBP_RELEASE_CHANNEL'
contains "${INSTALL}" 'Latest stable (recommended)'
contains "${INSTALL}" 'Latest pre-release'
contains "${INSTALL}" 'releases/latest/download/sbp-panel-update.json'
contains "${INSTALL}" 'releases?per_page=20'
contains "${INSTALL}" '/"prerelease":[[:space:]]*true/'
contains "${INSTALL}" 'SBP_RELEASE_CHANNEL must be stable or prerelease.'
contains "${INSTALL}" 'No installable pre-release was found.'
version_pattern='^[0-9]+\.[0-9]+\.[0-9]+$'
contains "${INSTALL}" "${version_pattern}"
for version in 0.0.1 0.1.0 1.0.0; do
  [[ "${version}" =~ ${version_pattern} ]] || fail "valid release version was rejected: ${version}"
done
for version in 1.0 1.0.0-pre.1 1.0.0-rc.1 1.0.0+build; do
  if [[ "${version}" =~ ${version_pattern} ]]; then
    fail "invalid release version was accepted: ${version}"
  fi
done

contains "${RELEASE}" "expected_entries=\"\$(printf '%s\\n' LICENSE NOTICE bootstrap.sh sbp-panel-linux-amd64 uninstall.sh | sort)\""
contains "${RELEASE}" 'test_size="$(stat -c'
contains "${RELEASE}" '"size":%s'
contains "${RELEASE}" 'Prerelease[[:space:]]*='
contains "${RELEASE}" 'release_flags=(--prerelease --latest=false)'
contains "${RELEASE}" 'release_flags=(--latest)'
if grep -Fq 'git archive' "${RELEASE}"; then
  fail "release bundle must not archive the repository"
fi
if grep -Eq 'apt-get clean|rm -rf /var/lib/apt/lists' "${INSTALL}" "${BOOTSTRAP}" || grep -Fq 'run("apt-get", "clean")' "${AGENT}"; then
  fail "SBP must not clear shared apt caches"
fi

test_root="$(mktemp -d)"
trap 'rm -rf "${test_root}"' EXIT
touch "${test_root}/panel.service"
existing_output="$(
  NO_COLOR=1 \
  SBP_PANEL_BIN="${test_root}/panel" \
  SBP_UPDATE_BIN="${test_root}/update" \
  SBP_PANEL_UNIT="${test_root}/panel.service" \
  SBP_AGENT_UNIT="${test_root}/agent.service" \
  bash "${INSTALL}"
)"
printf '%s' "${existing_output}" | grep -Fq 'already installed' || fail "existing SBP installation was not detected"
printf '%s' "${existing_output}" | grep -Fq 'sudo sbp-panel-update' || fail "existing SBP installation did not recommend the updater"
rm -rf "${test_root}"
trap - EXIT

contains "${BOOTSTRAP}" 'if [ ! -e "${CONFIG}" ] && [ ! -L "${CONFIG}" ]; then'
contains "${BOOTSTRAP}" 'mv -f "${CONFIG}.next" "${CONFIG}"'
contains "${BOOTSTRAP}" 'chown root:vpn-panel "${CONFIG}" "${ETC}/tls.crt" "${ETC}/tls.key"'
contains "${BOOTSTRAP}" 'ufw show added 2>/dev/null | grep -Fqx "${UFW_RULE_SPEC}"'
contains "${BOOTSTRAP}" 'mv -f "${UFW_RULE_MARKER}.tmp" "${UFW_RULE_MARKER}"'
contains "${BOOTSTRAP}" 'systemctl is-active --quiet vpn-panel-agent.service'
contains "${BOOTSTRAP}" 'https://${PANEL_URL_HOST}:${PANEL_PORT}/api/bootstrap/status'
contains "${BOOTSTRAP}" 'SBP Panel did not become healthy within 30 seconds.'
contains "${BOOTSTRAP}" 'PANEL_URL="https://${PANEL_DISPLAY_HOST}:${PANEL_PORT}"'
contains "${BOOTSTRAP}" '48;2;239;155;71'
contains "${BOOTSTRAP}" 'echo "Simple Bridge Panel: ${PANEL_URL}"'
contains "${BOOTSTRAP}" 'ln -s /opt/vpn-panel/bin/vpn-panel /usr/local/sbin/sbp-panel-update.next'
contains "${BOOTSTRAP}" 'LICENSE_DIR=/usr/share/licenses/sbp-panel'
contains "${BOOTSTRAP}" 'install -m 0644 "${LICENSE_SOURCE}" "${LICENSE_DIR}/LICENSE.next"'
contains "${BOOTSTRAP}" 'install -m 0644 "${NOTICE_SOURCE}" "${LICENSE_DIR}/NOTICE.next"'
contains "${BOOTSTRAP}" 'Update:%s sudo sbp-panel-update'
contains "${BOOTSTRAP}" 'Uninstall:%s sudo sbp-panel-uninstall'
if grep -Fq 'echo "Simple Bridge Panel: https://${IP}:9443"' "${BOOTSTRAP}"; then
  fail "the printed panel URL must use the configured listener and port"
fi

contains "${UNINSTALL}" 'if [ ! -f "${UFW_RULE_MARKER}" ] || [ "$(cat "${UFW_RULE_MARKER}")" != "${UFW_RULE_SPEC}" ]; then'
contains "${UNINSTALL}" "ufw --force delete allow 9443/tcp comment 'Simple Bridge Panel'"
contains "${UNINSTALL}" 'systemctl stop sbp-panel-update-watchdog.timer'
contains "${UNINSTALL}" 'systemctl stop sbp-panel-update-watchdog.service'
contains "${UNINSTALL}" '/usr/local/sbin/sbp-panel-update'
contains "${UNINSTALL}" '/usr/share/licenses/sbp-panel'
contains "${UNINSTALL}" 'systemctl reset-failed \'
if grep -Eq 'systemctl reset-failed[[:space:]]*(2>/dev/null|\|\||$)' "${UNINSTALL}"; then
  fail "panel-only uninstall must not reset unrelated failed systemd units"
fi

if grep -Eq '^[[:space:]]+/opt/vpn-panel-managed([[:space:]\\]|$)|rm[^#]*/opt/vpn-panel-managed' "${UNINSTALL}"; then
  fail "panel-only uninstall must not remove /opt/vpn-panel-managed"
fi
if grep -Eq '^[[:space:]]+/etc/(sysctl|modules-load)\.d/|rm[^#]*/etc/(sysctl|modules-load)\.d/' "${UNINSTALL}"; then
  fail "panel-only uninstall must not remove network tuning"
fi
if grep -Eq '^[[:space:]]+/var/lib/(docker|containerd)([[:space:]\\]|$)|rm[^#]*/var/lib/(docker|containerd)' "${UNINSTALL}"; then
  fail "panel-only uninstall must not remove container data"
fi

echo "deploy script assertions passed"
