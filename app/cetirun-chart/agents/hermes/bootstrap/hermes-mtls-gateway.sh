#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -eu
if [ "${CETIRUN_INGRESS_MTLS:-}" != "true" ]; then
  echo "ERROR: managed ingress gateway requires mTLS authority" >&2
  exit 1
fi
case "${HERMES_HOME:-}" in
  */.hermes) runtime_home=${HERMES_HOME%/.hermes} ;;
  *) echo "ERROR: managed ingress gateway requires the bound Hermes HOME" >&2; exit 1 ;;
esac
PYTHON_BIN=${HERMES_PYTHON:-/usr/local/lib/hermes-agent/venv/bin/python}
"$PYTHON_BIN" /etc/cetirun/hermes-apply-config.py \
  --home "$runtime_home" --ingress-mtls --listeners-only
exec hermes gateway run --replace --accept-hooks --external-supervisor
