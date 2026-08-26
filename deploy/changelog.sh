#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
CHANGELOG="${2:-${SCRIPT_DIR}/../CHANGELOG.md}"
VERSION="${1:-}"

if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "usage: $0 X.Y.Z [changelog]" >&2
  exit 2
fi
if [ ! -f "${CHANGELOG}" ]; then
  echo "changelog not found: ${CHANGELOG}" >&2
  exit 1
fi

header_prefix="## [${VERSION}] - "
header_count="$(awk -v prefix="${header_prefix}" 'index($0, prefix) == 1 { count++ } END { print count + 0 }' "${CHANGELOG}")"
if [ "${header_count}" -ne 1 ]; then
  echo "expected exactly one changelog entry for ${VERSION}, found ${header_count}" >&2
  exit 1
fi
header="$(awk -v prefix="${header_prefix}" 'index($0, prefix) == 1 { print; exit }' "${CHANGELOG}")"
release_date="${header#"${header_prefix}"}"
if [[ ! "${release_date}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  echo "invalid changelog date for ${VERSION}: ${release_date}" >&2
  exit 1
fi

notes="$({
  awk -v header="${header}" '
    $0 == header { found = 1; next }
    found && /^## \[/ { exit }
    found { print }
  ' "${CHANGELOG}"
} | sed '/[^[:space:]]/,$!d')"

if ! grep -Eq '^- .+' <<<"${notes}"; then
  echo "changelog entry for ${VERSION} has no release bullets" >&2
  exit 1
fi

printf '%s\n' "${notes}"
