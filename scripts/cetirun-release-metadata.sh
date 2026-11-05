#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"

usage() {
  cat <<'EOF'
Usage: cetirun-release-metadata.sh version|registry

Print the default CetiRun release version. The Runtime Chart is the source of
truth for the shared image and Chart version.
The public MCR repository prefix is the default artifact registry.
EOF
}

case "${1:-}" in
  version)
    value="$(python3 "${script_dir}/cetirun-version.py" get)"
    ;;
  registry)
    value="$(python3 "${script_dir}/cetirun-version.py" registry)"
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

if [ -z "${value}" ]; then
  echo "ERROR: Failed to resolve the CetiRun release version." >&2
  exit 1
fi

printf '%s\n' "${value}"
