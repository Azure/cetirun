#!/usr/bin/env bash
# Quick dev deploy: build images, push to test ACR, package/push Helm chart,
# and create/patch an OpenClawInstance CR.
#
# Usage:
#   scripts/openclaw-dev-deploy.sh <alias>
#   scripts/openclaw-dev-deploy.sh <alias> --skip-build   # reuse existing images
#   scripts/openclaw-dev-deploy.sh <alias> --apply-only    # only apply the CR (no build/push)
#
# Environment overrides:
#   REGISTRY         ACR hostname         (default: cetiruntest.azurecr.io)
#   IMAGE_TAG        image tag override    (default: dev-<branch>-<sha>)
#   CHART_VERSION    chart version override(default: 0.0.0-dev-<branch>-<sha>)
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
context_dir="${repo_root}/app/cetirun-chart"

# ── Args ──────────────────────────────────────────────────────────────────
ALIAS="${1:-peni}"
MODE="${2:-full}"  # full | --skip-build | --apply-only

# When skipping build, IMAGE_TAG must be set explicitly to reuse existing images.
if [ "$MODE" = "--skip-build" ] && [ -z "${IMAGE_TAG:-}" ]; then
  echo "ERROR: IMAGE_TAG must be set when using --skip-build (to reuse existing images)" >&2
  echo "  Example: IMAGE_TAG=dev-feat-branch-abc1234 $0 $ALIAS --skip-build" >&2
  exit 1
fi

registry="${REGISTRY:-cetiruntest.azurecr.io}"

# ── Tag generation ────────────────────────────────────────────────────────
branch=$(git -C "$repo_root" rev-parse --abbrev-ref HEAD | sed 's|[/:]|-|g' | head -c 40)
sha=$(git -C "$repo_root" rev-parse --short=7 HEAD)
default_tag="dev-${branch}-${sha}"
image_tag="${IMAGE_TAG:-$default_tag}"
chart_version="${CHART_VERSION:-0.0.0-${default_tag}}"

echo "──────────────────────────────────────────────"
echo "  Registry:       ${registry}"
echo "  Image tag:      ${image_tag}"
echo "  Chart version:  ${chart_version}"
echo "  Alias:          ${ALIAS}"
echo "  Mode:           ${MODE}"
echo "──────────────────────────────────────────────"

# ── Build & push images ──────────────────────────────────────────────────
if [ "$MODE" != "--skip-build" ] && [ "$MODE" != "--apply-only" ]; then
  echo ""
  echo "==> Logging in to ${registry}"
  az acr login --name "${registry%%.*}" 2>/dev/null || true

  echo ""
  echo "==> Building and pushing CetiRun agent images:${image_tag}"
  REGISTRY="$registry" IMAGE_NAME=openclaw-gateway IMAGE_TAG="$image_tag" PUSH=true BUILD_HERMES=true \
    "$script_dir/cetirun-docker-build.sh"
fi

# ── Package & push Helm chart ────────────────────────────────────────────
if [ "$MODE" != "--apply-only" ]; then
  echo ""
  echo "==> Packaging Helm chart (version ${chart_version})"
  tmp_chart=$(mktemp -d)
  helm package "$context_dir" \
    --version "$chart_version" \
    --app-version "$image_tag" \
    -d "$tmp_chart"

  echo "==> Pushing Helm chart to oci://${registry}/helm/"
  helm push "${tmp_chart}"/cetirun-*.tgz "oci://${registry}/helm/"
  rm -rf "$tmp_chart"
fi

# ── Apply OpenClawInstance CR ─────────────────────────────────────────────
echo ""
echo "==> Applying OpenClawInstance for ${ALIAS}"
cat <<EOF | kubectl apply -f -
apiVersion: cetirun.microsoft.com/v1alpha1
kind: OpenClawInstance
metadata:
  name: ${ALIAS}
spec:
  gatewayTokenSecretRef: "gateway-token"
  user:
    alias: "${ALIAS}"
    displayName: "${ALIAS}"
    team: "aks"
  helm:
    version: "${chart_version}"
    values:
      openclaw:
        image:
          repository: "${registry}/openclaw-gateway"
          tag: "${image_tag}"
      hermes:
        image:
          repository: "${registry}/hermes-gateway"
          tag: "${image_tag}"
      desktop:
        image:
          repository: "${registry}/cetirun-desktop"
          tag: "${image_tag}"
EOF

echo ""
echo "──────────────────────────────────────────────"
echo "  Deployed!"
echo ""
echo "  Gateway image:  ${registry}/openclaw-gateway:${image_tag}"
echo "  Hermes image:   ${registry}/hermes-gateway:${image_tag}"
echo "  Desktop image:  ${registry}/cetirun-desktop:${image_tag}"
echo "  Helm chart:     oci://${registry}/helm/cetirun:${chart_version}"
echo "  CR:             OpenClawInstance/${ALIAS}"
echo ""
echo "  Watch progress:"
echo "    kubectl get openclawinstance ${ALIAS} -w"
echo "    kubectl -n cetirun-${ALIAS} get pods -w"
echo "──────────────────────────────────────────────"
