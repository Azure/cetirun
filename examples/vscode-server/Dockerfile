# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG BASE_IMAGE=cetirun.azurecr.io/cetirun-base:0.1.1
FROM ${BASE_IMAGE}
ARG TARGETARCH
ARG ACCEPT_VSCODE_SERVER_LICENSE_TERMS=false

USER root
WORKDIR /opt/cetirun-vscode
COPY install.py vscode-lock.json ./
RUN test "$ACCEPT_VSCODE_SERVER_LICENSE_TERMS" = true \
    && python3 install.py --arch "$TARGETARCH" --output /opt/vscode \
    && for tool in sh cp mkdir mv rm chmod mktemp find cmp git node; do command -v "$tool" >/dev/null || exit 1; done
COPY start.mjs proxy.mjs browser-storage.mjs ./
LABEL org.opencontainers.image.licenses="MIT AND LicenseRef-Microsoft-VS-Code-Server" \
      org.opencontainers.image.description="Official VS Code Server with GitHub Copilot for an individual CetiRun development workspace"
ENV HOME=/home/vibe VSCODE_SERVER_LICENSE_ACCEPTED=true
USER 1024:1024
WORKDIR /home/vibe
EXPOSE 8080
ENTRYPOINT ["node", "/opt/cetirun-vscode/start.mjs"]
