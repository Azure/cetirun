# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG BASE_IMAGE=cetirun.azurecr.io/cetirun-base:0.1.1
FROM ${BASE_IMAGE}
ARG VERSION=0.1.1
ARG GIT_SHA=unknown

USER root
WORKDIR /opt/cetirun-pi
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund --registry=https://registry.npmjs.org \
    && node --input-type=module -e 'await import("@earendil-works/pi-coding-agent")'
COPY server.mjs pi-runtime.mjs copilot-auth.mjs ./
RUN for tool in sh cp mkdir mv rm chmod mktemp find cmp; do command -v "$tool" >/dev/null || exit 1; done

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun" \
      org.opencontainers.image.description="Pi coding-agent Custom example with native GitHub Copilot authentication" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      org.opencontainers.image.licenses="MIT"
ENV HOME=/home/vibe PI_TELEMETRY=0
USER 1024:1024
WORKDIR /home/vibe
EXPOSE 8080
ENTRYPOINT ["node", "/opt/cetirun-pi/server.mjs"]
