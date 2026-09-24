# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG VERSION=dev
ARG GIT_SHA=unknown
ARG BASE_IMAGE=cetirun.azurecr.io/cetirun-base:latest
ARG OPENCLAW_VERSION=2026.6.35

# Pack OpenClaw from npm in a base-derived stage so npm/node versions match runtime.
FROM ${BASE_IMAGE} AS openclaw-source
ARG OPENCLAW_VERSION
# Override NPM_REGISTRY to use a different package mirror.
ARG NPM_REGISTRY=https://registry.npmjs.org/
COPY --chown=vibe:vibe .npmrc /home/vibe/.npmrc
RUN npm config set registry "${NPM_REGISTRY}" && \
    npm config set replace-registry-host npmjs && \
    mkdir -p /home/vibe/openclaw-pack && \
    npm --min-release-age=0 pack "openclaw@${OPENCLAW_VERSION}" --pack-destination /home/vibe/openclaw-pack

FROM ${BASE_IMAGE}
ARG VERSION
ARG GIT_SHA

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun"
LABEL org.opencontainers.image.description="CetiRun OpenClaw gateway image"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${GIT_SHA}"
LABEL org.opencontainers.image.licenses="MIT"

ARG OPENCLAW_VERSION
ARG CLAWHUB_VERSION=0.17.0
ARG NPM_REGISTRY=https://registry.npmjs.org/

ENV CLAWHUB_WORKDIR=/home/vibe/.openclaw/
ENV OPENCLAW_SERVICE_VERSION=$OPENCLAW_VERSION
ENV OPENCLAW_PLUGIN_STAGE_DIR=/usr/local/share/openclaw-runtime-deps

COPY --from=openclaw-source /home/vibe/openclaw-pack/openclaw-*.tgz /tmp/openclaw.tgz

RUN npm config set registry "${NPM_REGISTRY}" && \
    npm config set replace-registry-host npmjs && \
    cd /usr/local/share/npm-global/lib && \
    npm --min-release-age=0 install /tmp/openclaw.tgz clawhub@${CLAWHUB_VERSION} && \
    for b in node_modules/.bin/*; do \
      [ -L "$b" ] && ln -sf "$(readlink -f "$b")" /usr/local/share/npm-global/bin/$(basename "$b"); \
    done && \
    npm cache clean --force && \
    rm -f /tmp/openclaw.tgz

# Keep extensions and npm plugins outside /home/vibe so PVC init copies only symlinks.
RUN mkdir -p /usr/local/share/openclaw-extensions /usr/local/share/openclaw-npm \
             /home/vibe/.openclaw && \
    ln -s /usr/local/share/openclaw-extensions /home/vibe/.openclaw/extensions && \
    ln -s /usr/local/share/openclaw-npm /home/vibe/.openclaw/npm

# Install plugins unbundled in 2026.5.x from npm, pinned to the OpenClaw version.
# After install, prune non-linux-x64 optional platform binaries that openclaw
# plugins install pulls for all architectures (~2.5 GB of unnecessary files).
# Remove generated identity in the same layer so instances do not share a device key.
# Warm ACPX dependencies outside OpenClaw's five-minute installer timeout.
RUN npm --min-release-age=0 install \
      --prefix /tmp/openclaw-acpx-prefetch \
      --omit=dev \
      --omit=peer \
      --legacy-peer-deps \
      --ignore-scripts \
      --no-audit \
      --no-fund \
      --package-lock=false \
      --save=false \
      "@openclaw/acpx@${OPENCLAW_VERSION}" && \
    rm -rf /tmp/openclaw-acpx-prefetch && \
    openclaw plugins install "npm:@openclaw/msteams@${OPENCLAW_VERSION}" && \
    openclaw plugins install "npm:@openclaw/acpx@${OPENCLAW_VERSION}" && \
    find /usr/local/share/openclaw-npm -type d \( \
      -name "*-darwin-*" -o -name "*-win32-*" \
      -o -name "*-linux-arm64" -o -name "*-linux-arm64-musl" \
      -o -name "*-linux-x64-musl" \
    \) -prune -exec rm -rf {} + && \
    (openclaw doctor --fix --non-interactive --yes || true) && \
    rm -rf /home/vibe/.openclaw/identity && \
    rm -f /home/vibe/.openclaw/openclaw.json && \
    npm cache clean --force

# Seed OpenClaw personality files into the workspace skeleton.
COPY --chown=vibe:vibe agents/openclaw/personality/ /home/vibe/.openclaw/workspace/

COPY --chown=vibe:vibe agents/openclaw/bootstrap/entrypoint.sh /usr/local/bin/entrypoint.sh

# In-pod auth helper service (shared with Hermes). Runs in its own tiny Python
# venv so we don't pollute the system Python; venv lives outside /home/vibe so
# the PVC overlay doesn't shadow it. OpenClaw doesn't otherwise need Python deps.
ARG CETIRUN_AUTH_AIOHTTP_VERSION=3.14.3
ARG CETIRUN_AUTH_PYYAML_VERSION=6.0.2
USER root
RUN python3 -m venv /usr/local/lib/cetirun-auth && \
    /usr/local/lib/cetirun-auth/bin/python -m pip install \
      --no-cache-dir \
      --root-user-action=ignore \
      "aiohttp==${CETIRUN_AUTH_AIOHTTP_VERSION}" \
      "pyyaml==${CETIRUN_AUTH_PYYAML_VERSION}"
COPY --chown=vibe:vibe configs/cetirun-auth-service.py /usr/local/share/cetirun/cetirun-auth-service.py
RUN chmod 755 /usr/local/share/cetirun/cetirun-auth-service.py
USER vibe

EXPOSE 18789 7681 3978 18790

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
