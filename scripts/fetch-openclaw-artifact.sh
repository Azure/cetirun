#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

destination_dir="${1:?usage: fetch-openclaw-artifact.sh DESTINATION_DIR}"
version="2026.9.6"
url="https://registry.npmjs.org/openclaw/-/openclaw-${version}.tgz"
sha256="1a7355691bc0e605222ba818f1f72c1787253c78dfeb0df6be2086ec73b71e63"
integrity="sha512-Ie0kyQSCVfFqixsgVg39vevUDq01Ch5u3+7Yu5Y3qARczmdAe+lzp8bVnO9925rHiW/+CFp70zfORCyPmCH31g=="
artifact="${destination_dir}/openclaw.tgz"

mkdir -p "${destination_dir}"
curl -fsSL --retry 5 --retry-all-errors --connect-timeout 30 --max-time 300 \
  "${url}" -o "${artifact}"
printf '%s  %s\n' "${sha256}" "${artifact}" | sha256sum --check
node -e '
  const crypto = require("crypto");
  const fs = require("fs");
  const actual = "sha512-" + crypto.createHash("sha512")
    .update(fs.readFileSync(process.argv[1])).digest("base64");
  if (actual !== process.argv[2]) throw new Error("OpenClaw tarball integrity mismatch");
' "${artifact}" "${integrity}"
