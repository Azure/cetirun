# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG BASE_IMAGE=cetirun.azurecr.io/cetirun-base:latest

FROM ${BASE_IMAGE}

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun"
LABEL org.opencontainers.image.description="CetiRun Hermes gateway image"
LABEL org.opencontainers.image.licenses="MIT"

# v0.16.0 — pinned by PyPI wheel SHA256 for supply-chain integrity
ARG HERMES_VERSION=0.16.0
ARG HERMES_WHEEL_SHA256=accb5a4a4827b41b3d162d2eb0b5f6db585d942ee23a3678ef21fc94d21c34a2
ARG PYPI_INDEX_URL=https://pypi.org/simple/

ENV HERMES_HOME=/home/vibe/.hermes
ENV PATH=/usr/local/lib/hermes-agent/bin:$PATH

# Pinned versions for the cetirun_teams plugin's auth deps.
ARG AZURE_IDENTITY_VERSION=1.19.0
ARG PYJWT_VERSION=2.10.1

RUN uv venv /usr/local/lib/hermes-agent --python python3 && \
    printf '%s\n' "hermes-agent[all]==${HERMES_VERSION} --hash=sha256:${HERMES_WHEEL_SHA256}" > /tmp/requirements.txt && \
    uv pip install --python /usr/local/lib/hermes-agent/bin/python \
      --index-url "${PYPI_INDEX_URL}" \
      -r /tmp/requirements.txt && \
    rm -f /tmp/requirements.txt && \
    uv pip install --python /usr/local/lib/hermes-agent/bin/python \
      --index-url "${PYPI_INDEX_URL}" \
      "azure-identity==${AZURE_IDENTITY_VERSION}" "pyjwt[crypto]==${PYJWT_VERSION}"

# Install cetirun-teams platform plugin into Hermes's bundled plugin path.
# Hermes discovers platform plugins at <site-packages>/plugins/platforms/<name>/.
COPY --chown=vibe:vibe agents/hermes/plugins/platforms/cetirun_teams/ /tmp/cetirun_teams_plugin/
RUN PLUGINS_DIR=$(/usr/local/lib/hermes-agent/bin/python -c \
      "import plugins; import pathlib; print(pathlib.Path(plugins.__path__[0]) / 'platforms')") && \
    cp -a /tmp/cetirun_teams_plugin "${PLUGINS_DIR}/cetirun_teams" && \
    rm -rf /tmp/cetirun_teams_plugin
COPY --chown=vibe:vibe agents/hermes/bootstrap/hermes-auth.sh /usr/local/share/cetirun/hermes-auth.sh
COPY --chown=vibe:vibe agents/hermes/bootstrap/hermes-copilot-auth.py /usr/local/share/cetirun/hermes-copilot-auth.py
COPY --chown=vibe:vibe configs/cetirun-auth-service.py /usr/local/share/cetirun/cetirun-auth-service.py
COPY --chown=vibe:vibe agents/hermes/bootstrap/hermes-entrypoint.sh /usr/local/bin/hermes-entrypoint.sh
RUN chmod 755 /usr/local/bin/hermes-entrypoint.sh \
    /usr/local/share/cetirun/hermes-auth.sh \
    /usr/local/share/cetirun/hermes-copilot-auth.py \
    /usr/local/share/cetirun/cetirun-auth-service.py

EXPOSE 8642 7681 9119 18790

ENTRYPOINT ["/usr/local/bin/hermes-entrypoint.sh"]
