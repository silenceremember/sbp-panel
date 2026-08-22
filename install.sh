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

fail() {
  printf '%s%s%s\n' "${SBP_DANGER}" "$1" "${SBP_RESET}" >&2
  exit 1
}

PANEL_BIN="${SBP_PANEL_BIN:-/opt/vpn-panel/bin/vpn-panel}"
UPDATE_BIN="${SBP_UPDATE_BIN:-/usr/local/sbin/sbp-panel-update}"
PANEL_UNIT="${SBP_PANEL_UNIT:-/etc/systemd/system/vpn-panel.service}"
AGENT_UNIT="${SBP_AGENT_UNIT:-/etc/systemd/system/vpn-panel-agent.service}"
if [ -x "${PANEL_BIN}" ] || [ -x "${UPDATE_BIN}" ] || [ -e "${PANEL_UNIT}" ] || [ -e "${AGENT_UNIT}" ]; then
  printf '%s%sSimple Bridge Panel is already installed.%s No files were changed.\n' "${SBP_ACCENT}" "${SBP_BOLD}" "${SBP_RESET}"
  printf '%sUpdate:%s sudo sbp-panel-update\n' "${SBP_ACCENT}" "${SBP_RESET}"
  exit 0
fi

if [ "$(id -u)" -ne 0 ]; then
  fail "Run as root: curl -fsSL https://raw.githubusercontent.com/silenceremember/sbp-panel/main/install.sh | sudo bash"
fi

RELEASE_BASE_URL="${SBP_RELEASE_BASE_URL:-https://github.com/silenceremember/sbp-panel/releases/download}"
STABLE_METADATA_URL="${SBP_STABLE_METADATA_URL:-https://github.com/silenceremember/sbp-panel/releases/latest/download/sbp-panel-update.json}"
RELEASES_API_URL="${SBP_RELEASES_API_URL:-https://api.github.com/repos/silenceremember/sbp-panel/releases?per_page=20}"
WORKDIR="$(mktemp -d)"
INSTALLED_UNZIP=0
MAX_METADATA_BYTES=65536
MAX_ARCHIVE_BYTES=$((128 * 1024 * 1024))
MAX_RELEASES_BYTES=$((1024 * 1024))
cleanup() {
  rm -rf "${WORKDIR}"
  if [ "${INSTALLED_UNZIP}" -eq 1 ]; then
    apt-get purge -y unzip >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

release_channel="${SBP_RELEASE_CHANNEL:-}"
if [ -n "${SBP_METADATA_URL:-}" ]; then
  METADATA_URL="${SBP_METADATA_URL}"
  release_channel=custom
else
  if [ -z "${release_channel}" ] && [ -r /dev/tty ]; then
    printf '%s%sRelease channel%s\n' "${SBP_ACCENT}" "${SBP_BOLD}" "${SBP_RESET}"
    printf '  1) Latest stable (recommended)\n'
    printf '  2) Latest pre-release\n'
    printf '%sChoose [1]: %s' "${SBP_ACCENT}" "${SBP_RESET}"
    IFS= read -r release_choice </dev/tty || fail "Could not read the release channel."
    case "${release_choice}" in
      "" | 1) release_channel=stable ;;
      2) release_channel=prerelease ;;
      *) fail "Choose 1 for stable or 2 for pre-release." ;;
    esac
  fi
  release_channel="${release_channel:-stable}"

  case "${release_channel}" in
    stable)
      METADATA_URL="${STABLE_METADATA_URL}"
      ;;
    prerelease)
      releases_file="${WORKDIR}/releases.json"
      if ! curl -fsSL --retry 3 --connect-timeout 15 --max-time 60 \
        --max-filesize "${MAX_RELEASES_BYTES}" \
        -H 'Accept: application/vnd.github+json' \
        -H 'X-GitHub-Api-Version: 2022-11-28' \
        "${RELEASES_API_URL}" -o "${releases_file}"; then
        fail "Could not load the GitHub release list."
      fi
      [ "$(stat -c '%s' "${releases_file}")" -le "${MAX_RELEASES_BYTES}" ] \
        || fail "The GitHub release list is too large."
      prerelease_tag="$(awk '
        /"tag_name":[[:space:]]*"/ {
          tag = $0
          sub(/^.*"tag_name":[[:space:]]*"/, "", tag)
          sub(/".*$/, "", tag)
        }
        /"prerelease":[[:space:]]*true/ && tag != "" {
          print tag
          exit
        }
      ' "${releases_file}")"
      [[ "${prerelease_tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
        || fail "No installable pre-release was found."
      METADATA_URL="${RELEASE_BASE_URL}/${prerelease_tag}/sbp-panel-update.json"
      ;;
    *)
      fail "SBP_RELEASE_CHANNEL must be stable or prerelease."
      ;;
  esac
fi

printf '%sRelease channel:%s %s\n' "${SBP_ACCENT}" "${SBP_RESET}" "${release_channel}"
if ! metadata="$(curl -fsSL --retry 3 --connect-timeout 15 --max-time 60 "${METADATA_URL}" | head -c $((MAX_METADATA_BYTES + 1)))"; then
  if [ "${release_channel}" = stable ]; then
    fail "Could not load a stable release. Re-run the installer and choose pre-release if appropriate."
  fi
  fail "Could not load release metadata."
fi
[ "${#metadata}" -le "${MAX_METADATA_BYTES}" ] || fail "The release metadata is too large."
version="$(printf '%s' "${metadata}" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')"
asset="$(printf '%s' "${metadata}" | sed -n 's/.*"asset_name":"\([^"]*\)".*/\1/p')"
expected_sha="$(printf '%s' "${metadata}" | sed -n 's/.*"sha256":"\([0-9a-fA-F]*\)".*/\1/p' | tr 'A-F' 'a-f')"
expected_size="$(printf '%s' "${metadata}" | sed -n 's/.*"size":\([0-9][0-9]*\).*/\1/p')"

if ! [[ "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] \
  || [ "${asset}" != "sbp-panel-${version}-linux-amd64.zip" ] \
  || ! [[ "${expected_sha}" =~ ^[0-9a-f]{64}$ ]] \
  || ! [[ "${expected_size}" =~ ^[0-9]+$ ]] \
  || [ "${expected_size}" -le 0 ] \
  || [ "${expected_size}" -gt "${MAX_ARCHIVE_BYTES}" ]; then
  fail "The release metadata is invalid."
fi

archive="${WORKDIR}/${asset}"
if ! curl -fL --retry 3 --connect-timeout 15 --max-time 600 \
  "${RELEASE_BASE_URL}/v${version}/${asset}" | head -c $((MAX_ARCHIVE_BYTES + 1)) > "${archive}"; then
  fail "The release download failed or exceeded the size limit."
fi

actual_size="$(stat -c '%s' "${archive}")"
if [ "${actual_size}" != "${expected_size}" ]; then
  fail "Release size verification failed."
fi

actual_sha="$(sha256sum "${archive}" | awk '{print $1}')"
if [ "${actual_sha}" != "${expected_sha}" ]; then
  fail "Release checksum verification failed."
fi

if ! command -v unzip >/dev/null; then
  apt-get update
  apt-get install -y --no-install-recommends unzip ca-certificates
  INSTALLED_UNZIP=1
fi

bundle="${WORKDIR}/bundle"
mkdir -p "${bundle}"
unzip -q "${archive}" -d "${bundle}"
if [ ! -f "${bundle}/bootstrap.sh" ] || [ ! -x "${bundle}/sbp-panel-linux-amd64" ]; then
  fail "The verified release bundle is incomplete."
fi

printf '%sVerified release v%s.%s\n' "${SBP_SUCCESS}" "${version}" "${SBP_RESET}"
printf '%s%sInstalling Simple Bridge Panel v%s...%s\n' "${SBP_ACCENT}" "${SBP_BOLD}" "${version}" "${SBP_RESET}"
if [ -r /dev/tty ]; then
  bash "${bundle}/bootstrap.sh" </dev/tty
else
  fail "An interactive terminal is required to set the administrator password."
fi
