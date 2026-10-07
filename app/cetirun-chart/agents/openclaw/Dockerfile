# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG VERSION=dev
ARG GIT_SHA=unknown
ARG BASE_IMAGE=cetirun.azurecr.io/cetirun-base:latest
ARG OPENCLAW_VERSION=2026.9.6

# The build driver supplies openclaw-artifact as a named context after fetching
# and verifying the immutable npm tarball on the build host. Verify its SRI
# again in the target environment before installing it.
FROM ${BASE_IMAGE} AS openclaw-source
ARG OPENCLAW_VERSION
ARG OPENCLAW_TARBALL_INTEGRITY=sha512-Ie0kyQSCVfFqixsgVg39vevUDq01Ch5u3+7Yu5Y3qARczmdAe+lzp8bVnO9925rHiW/+CFp70zfORCyPmCH31g==
COPY --from=openclaw-artifact --chown=vibe:vibe /openclaw.tgz /home/vibe/openclaw-pack/openclaw.tgz
RUN test "$OPENCLAW_VERSION" = "2026.9.6" && \
    node -e 'const crypto=require("crypto"),fs=require("fs"); const actual="sha512-"+crypto.createHash("sha512").update(fs.readFileSync(process.argv[1])).digest("base64"); if(actual!==process.argv[2]) throw new Error("OpenClaw tarball integrity mismatch")' \
      /home/vibe/openclaw-pack/openclaw.tgz "$OPENCLAW_TARBALL_INTEGRITY"

FROM ${BASE_IMAGE}
ARG VERSION
ARG GIT_SHA

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun"
LABEL org.opencontainers.image.description="CetiRun OpenClaw gateway image"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${GIT_SHA}"
LABEL org.opencontainers.image.licenses="MIT"

ARG OPENCLAW_VERSION
ARG CLAWHUB_VERSION=0.23.3
ARG NPM_REGISTRY=https://registry.npmjs.org/

ENV CLAWHUB_WORKDIR=/home/vibe/.openclaw/
ENV OPENCLAW_SERVICE_VERSION=$OPENCLAW_VERSION

COPY --from=openclaw-source /home/vibe/openclaw-pack/openclaw.tgz /tmp/openclaw.tgz

RUN npm config set registry "${NPM_REGISTRY}" && \
    npm config set replace-registry-host npmjs && \
    cd /usr/local/share/npm-global/lib && \
    npm pkg set 'allowScripts.openclaw=true' --json && \
    npm --min-release-age=0 install /tmp/openclaw.tgz clawhub@${CLAWHUB_VERSION} && \
    for b in node_modules/.bin/*; do \
      [ -L "$b" ] && ln -sf "$(readlink -f "$b")" /usr/local/share/npm-global/bin/$(basename "$b"); \
    done && \
    npm cache clean --force && \
    rm -f /tmp/openclaw.tgz

# Project installs identify a local tarball by its trusted file: source, not by
# the package name. Let npm write that exact approval, run only OpenClaw's
# reviewed lifecycle, and prove that the package's pending marker was retired.
RUN cd /usr/local/share/npm-global/lib && \
    npm pkg delete allowScripts.openclaw && \
    npm approve-scripts openclaw && \
    npm rebuild openclaw && \
    test ! -e node_modules/openclaw/.openclaw-lifecycle-pending && \
    set -- $(openclaw --version) && \
    test "$1" = OpenClaw && \
    test "$2" = "${OPENCLAW_VERSION}"

# Keep extensions and npm plugins outside /home/vibe so PVC init copies only symlinks.
RUN mkdir -p /usr/local/share/openclaw-extensions /usr/local/share/openclaw-npm \
             /home/vibe/.openclaw && \
    ln -s /usr/local/share/openclaw-extensions /home/vibe/.openclaw/extensions && \
    ln -s /usr/local/share/openclaw-npm /home/vibe/.openclaw/npm

# Install every external plugin exposed by the CetiRun OpenClaw values, pinned
# to the OpenClaw version. Telegram is already included in the core package.
# After install, prune non-linux-x64 optional platform binaries that openclaw
# plugins install pulls for other architectures.
# Remove generated identity in the same layer so instances do not share a device key.
# Warm the npm cache before the managed installs. The managed installer has a
# five-minute work timeout with no CLI override; an ARM host building the
# required linux/amd64 image under QEMU can exceed it even while npm is healthy.
RUN npm --min-release-age=0 install \
      --prefix /tmp/openclaw-plugin-prefetch \
      --omit=dev \
      --omit=peer \
      --legacy-peer-deps \
      --ignore-scripts \
      --no-audit \
      --no-fund \
      --package-lock=false \
      --save=false \
      "@openclaw/acpx@${OPENCLAW_VERSION}" \
      "@openclaw/brave-plugin@${OPENCLAW_VERSION}" \
      "@openclaw/discord@${OPENCLAW_VERSION}" \
      "@openclaw/feishu@${OPENCLAW_VERSION}" \
      "@openclaw/msteams@${OPENCLAW_VERSION}" \
      "@openclaw/perplexity-plugin@${OPENCLAW_VERSION}" \
      "@openclaw/slack@${OPENCLAW_VERSION}" && \
    rm -rf /tmp/openclaw-plugin-prefetch && \
    openclaw plugins install "npm:@openclaw/acpx@${OPENCLAW_VERSION}" && \
    openclaw plugins install "npm:@openclaw/brave-plugin@${OPENCLAW_VERSION}" && \
    openclaw plugins install "npm:@openclaw/discord@${OPENCLAW_VERSION}" && \
    openclaw plugins install "npm:@openclaw/feishu@${OPENCLAW_VERSION}" && \
    openclaw plugins install "npm:@openclaw/msteams@${OPENCLAW_VERSION}" && \
    openclaw plugins install "npm:@openclaw/perplexity-plugin@${OPENCLAW_VERSION}" && \
    openclaw plugins install "npm:@openclaw/slack@${OPENCLAW_VERSION}" && \
    find /usr/local/share/openclaw-npm -type d \( \
      -name "*-darwin-*" -o -name "*-win32-*" \
      -o -name "*-linux-arm64" -o -name "*-linux-arm64-musl" \
      -o -name "*-linux-x64-musl" \
    \) -prune -exec rm -rf {} + && \
    rm -rf /home/vibe/.openclaw/identity && \
    rm -f /home/vibe/.openclaw/openclaw.json && \
    npm cache clean --force

# Seed OpenClaw personality files into the workspace skeleton.
COPY --chown=vibe:vibe agents/openclaw/personality/ /home/vibe/.openclaw/workspace/
COPY --chown=vibe:vibe skills/azure/ /home/vibe/.openclaw/skills/azure/
COPY --chown=vibe:vibe configs/cetirun-agent /usr/local/bin/cetirun-agent

COPY --chown=vibe:vibe agents/openclaw/bootstrap/entrypoint.sh /usr/local/bin/entrypoint.sh

USER root
COPY --chown=vibe:vibe configs/builtin-skill-sync.sh /usr/local/share/cetirun/builtin-skill-sync.sh
RUN chmod 755 /usr/local/share/cetirun/builtin-skill-sync.sh \
    /usr/local/bin/cetirun-agent \
    /usr/local/bin/entrypoint.sh
USER vibe

# ClawHub 0.23.3 and OpenAI 7.x still resolve undici 7.29.0
# (GHSA-3wwx-pv8p-q78v). Keep this one major-scoped override until their
# published manifests require the fixed 7.29.1 or later release.
ARG CLAWHUB_UNDICI_VERSION=7.29.1
RUN --mount=from=openclaw-source,source=/home/vibe/openclaw-pack/openclaw.tgz,target=/tmp/openclaw.tgz,ro \
    cd /usr/local/share/npm-global/lib && \
    npm pkg set "overrides[undici@7]=${CLAWHUB_UNDICI_VERSION}" && \
    npm install --omit=dev --no-audit --no-fund && \
    npm ls "undici@${CLAWHUB_UNDICI_VERSION}" --all >/dev/null && \
    ! npm ls undici@7.29.0 --all >/dev/null 2>&1

EXPOSE 18789 7681 3978

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
