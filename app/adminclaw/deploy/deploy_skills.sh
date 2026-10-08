#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Copy skills to the admin pod
set -euo pipefail

NAMESPACE="${NAMESPACE:-cetirun-admin}"
POD="${POD:-openclaw-admin-cetirun-0}"
CONTAINER="${CONTAINER:-cetirun}"
REMOTE_DIR="/home/vibe/.openclaw/skills"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL_SKILLS_DIR="$SCRIPT_DIR/../skills"

echo "Copying skills to ${POD} in namespace ${NAMESPACE}..."
kubectl cp "$LOCAL_SKILLS_DIR" "$NAMESPACE/$POD:$REMOTE_DIR" -c "$CONTAINER"

echo "Verifying..."
kubectl exec -n "$NAMESPACE" "$POD" -c "$CONTAINER" -- find "$REMOTE_DIR" -type f

echo "Done."
