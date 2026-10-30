# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG BASE_IMAGE=cetirun.azurecr.io/cetirun-base:latest
ARG HERMES_VERSION=0.21.5
ARG HERMES_RELEASE=2026.9.24
ARG HERMES_GIT_SHA=f97608f178d1ffeca59860195ab7da295f7c8e5f
ARG HERMES_SOURCE_SHA256=15b15ce4e6ec8ea424a081823709d1e17f0943e7b42b59597d24ebb94cbd1742
ARG HERMES_UV_IMAGE=ghcr.io/astral-sh/uv:0.11.6@sha256:43cb71695fcad1516c2fbe0f56e500184c42d8bce838d9f64593b8aff2c16298
ARG HERMES_NODE_IMAGE=node:26-bookworm-slim@sha256:9e6f9357d371591e32ab6f2d8a26d63bdd0d17c29eee3f4f3e7e454d9634bf73
ARG HERMES_SQLITE_VERSION=3530400
ARG HERMES_SQLITE_SHA256=0e9483900e92cd5de8fd48d16bf9200145a61f7fd5be542a5ac81d8a9516eb9c

FROM ${BASE_IMAGE} AS hermes-source
ARG HERMES_VERSION
ARG HERMES_RELEASE
ARG HERMES_SOURCE_SHA256

USER vibe
WORKDIR /usr/local/lib/hermes-agent
# Let the pinned archive finish on slow runners instead of repeatedly restarting it.
RUN curl -fsSL --retry 3 --retry-all-errors --retry-delay 15 --retry-max-time 1800 \
      --connect-timeout 15 --max-time 1800 \
      "https://codeload.github.com/NousResearch/hermes-agent/tar.gz/refs/tags/v${HERMES_RELEASE}" \
      -o /tmp/hermes-agent.tar.gz && \
    echo "${HERMES_SOURCE_SHA256}  /tmp/hermes-agent.tar.gz" | sha256sum --check && \
    tar xzf /tmp/hermes-agent.tar.gz --strip-components=1 -C /usr/local/lib/hermes-agent && \
    rm /tmp/hermes-agent.tar.gz && \
    test "$(python3 -c 'import pathlib, tomllib; print(tomllib.loads(pathlib.Path("pyproject.toml").read_text())["project"]["version"])')" = "${HERMES_VERSION}"

FROM ${HERMES_UV_IMAGE} AS hermes-uv

FROM ${BASE_IMAGE} AS hermes-sqlite-build
ARG HERMES_SQLITE_VERSION
ARG HERMES_SQLITE_SHA256

USER root
RUN tdnf install -y binutils gcc glibc-devel kernel-headers make && \
    tdnf clean all
RUN (curl -fsSL --retry 1 --retry-all-errors --connect-timeout 15 --max-time 60 \
      -o /tmp/sqlite.tar.gz \
      "https://sqlite.org/2026/sqlite-autoconf-${HERMES_SQLITE_VERSION}.tar.gz" || \
     curl -fsSL --retry 3 --retry-all-errors --connect-timeout 15 --max-time 120 \
      -o /tmp/sqlite.tar.gz \
      "https://sources.buildroot.net/sqlite/sqlite-autoconf-${HERMES_SQLITE_VERSION}.tar.gz") && \
    echo "${HERMES_SQLITE_SHA256}  /tmp/sqlite.tar.gz" | sha256sum --check && \
    tar xzf /tmp/sqlite.tar.gz -C /tmp && \
    cd "/tmp/sqlite-autoconf-${HERMES_SQLITE_VERSION}" && \
    CFLAGS="-O2 \
      -DSQLITE_ENABLE_FTS3 \
      -DSQLITE_ENABLE_FTS3_PARENTHESIS \
      -DSQLITE_ENABLE_FTS4 \
      -DSQLITE_ENABLE_FTS5 \
      -DSQLITE_ENABLE_RTREE \
      -DSQLITE_ENABLE_GEOPOLY \
      -DSQLITE_ENABLE_COLUMN_METADATA \
      -DSQLITE_ENABLE_UNLOCK_NOTIFY \
      -DSQLITE_ENABLE_DBSTAT_VTAB \
      -DSQLITE_ENABLE_DBPAGE_VTAB \
      -DSQLITE_ENABLE_MATH_FUNCTIONS \
      -DSQLITE_ENABLE_PREUPDATE_HOOK \
      -DSQLITE_ENABLE_SESSION \
      -DSQLITE_SECURE_DELETE \
      -DSQLITE_THREADSAFE=1 \
      -DSQLITE_MAX_VARIABLE_NUMBER=250000" \
      ./configure --prefix=/opt/sqlite-fixed --disable-static && \
    make -j"$(nproc)" && \
    make install

FROM --platform=$BUILDPLATFORM ${HERMES_NODE_IMAGE} AS hermes-ui-build
ARG NPM_REGISTRY=https://registry.npmjs.org/

WORKDIR /opt/hermes
COPY --from=hermes-source /usr/local/lib/hermes-agent/ /opt/hermes/

RUN npm_config_registry="${NPM_REGISTRY}" \
      npm install --workspace ui-tui --workspace web --include-workspace-root \
        --include=dev --silent --no-fund --no-audit && \
    npm --prefix web run build -- --base=./ && \
    npm --prefix ui-tui run build

FROM ${BASE_IMAGE} AS hermes-build
ARG HERMES_VERSION
ARG HERMES_GIT_SHA
ARG PYPI_INDEX_URL=https://pypi.org/simple/

COPY --from=hermes-uv /uv /usr/local/bin/uv
COPY --from=hermes-uv /uvx /usr/local/bin/uvx

ENV HERMES_HOME=/home/vibe/.hermes
ENV HERMES_INSTALL_DIR=/usr/local/lib/hermes-agent
ENV HERMES_PYTHON=/usr/local/lib/hermes-agent/venv/bin/python
ENV UV_PROJECT_ENVIRONMENT=/usr/local/lib/hermes-agent/venv
ENV PATH=/usr/local/lib/hermes-agent/venv/bin:$PATH

WORKDIR /usr/local/lib/hermes-agent

# Follow Hermes' official source-install contract: install the immutable release
# checkout with its checked-in uv.lock instead of reconstructing dependencies
# from a PyPI wheel. Browser binaries are omitted because CetiRun supplies a
# separate Chrome/CDP desktop sidecar.
COPY --from=hermes-source --chown=vibe:vibe \
  /usr/local/lib/hermes-agent/ /usr/local/lib/hermes-agent/

RUN UV_DEFAULT_INDEX="${PYPI_INDEX_URL}" \
      uv sync --python /usr/bin/python3 --frozen --extra all \
        --extra anthropic --extra azure-identity && \
    printf '%s\n' docker > .install_method && \
    printf '%s\n' "${HERMES_GIT_SHA}" > .hermes_build_sha

COPY --from=hermes-ui-build --chown=vibe:vibe \
  /opt/hermes/hermes_cli/web_dist/ /usr/local/lib/hermes-agent/hermes_cli/web_dist/
COPY --from=hermes-ui-build --chown=vibe:vibe \
  /opt/hermes/ui-tui/dist/ /usr/local/lib/hermes-agent/ui-tui/dist/

COPY --chown=vibe:vibe agents/hermes/compat/workload_mcp.py /usr/local/lib/hermes-agent/cetirun_compat/workload_mcp.py
COPY --chown=vibe:vibe agents/hermes/compat/apply_workload_mcp.py /tmp/apply-workload-mcp.py
RUN python3 /tmp/apply-workload-mcp.py --root /usr/local/lib/hermes-agent && rm /tmp/apply-workload-mcp.py

# Install the CetiRun-specific Teams adapter as a normal bundled Hermes plugin.
COPY --chown=vibe:vibe agents/hermes/plugins/platforms/cetirun_teams/ \
  /usr/local/lib/hermes-agent/plugins/platforms/cetirun_teams/

RUN test -x "${HERMES_PYTHON}" && \
    "${HERMES_PYTHON}" -c \
      'import importlib.metadata, pathlib; assert importlib.metadata.version("hermes-agent") == "0.21.5"; assert pathlib.Path("hermes_cli/web_dist/index.html").is_file(); assert pathlib.Path("ui-tui/dist/entry.js").is_file()'

FROM ${BASE_IMAGE}
ARG VERSION=dev
ARG GIT_SHA=unknown
ARG HERMES_VERSION
ARG HERMES_GIT_SHA

COPY --from=hermes-build --chown=vibe:vibe /usr/local/lib/hermes-agent/ /usr/local/lib/hermes-agent/
COPY --from=hermes-sqlite-build /opt/sqlite-fixed/lib/libsqlite3.so.3.53.4 /usr/local/lib/
COPY --chown=vibe:vibe agents/hermes/compat/ /usr/local/lib/hermes-agent/cetirun_compat/
COPY --chown=vibe:vibe agents/hermes/plugins/cetirun-memory-context/ /usr/local/lib/hermes-agent/plugins/cetirun-memory-context/
RUN python3 /usr/local/lib/hermes-agent/cetirun_compat/apply.py --root /usr/local/lib/hermes-agent

USER root
RUN ln -sf libsqlite3.so.3.53.4 /usr/local/lib/libsqlite3.so.0 && \
    ln -sf libsqlite3.so.3.53.4 /usr/local/lib/libsqlite3.so && \
    printf '/usr/local/lib\n' > /etc/ld.so.conf.d/000-sqlite-fixed.conf && \
    ldconfig && \
    python3 -c 'import sqlite3; assert sqlite3.sqlite_version_info >= (3, 51, 3)' && \
    mkdir -p /etc/hermes && \
    printf '{"deployment_kind":"image","image":"cetirun/hermes-gateway","manager":"docker","revision":"%s","schema":1,"version":"%s"}\n' \
      "${HERMES_GIT_SHA}" "${HERMES_VERSION}" > /etc/hermes/image-provenance.json && \
    chmod 0444 /etc/hermes/image-provenance.json
USER vibe

WORKDIR /home/vibe

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun"
LABEL org.opencontainers.image.description="CetiRun Hermes gateway image"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${GIT_SHA}"
LABEL org.opencontainers.image.licenses="MIT"
LABEL io.cetirun.hermes.version="${HERMES_VERSION}"
LABEL io.cetirun.hermes.revision="${HERMES_GIT_SHA}"

ENV HERMES_HOME=/home/vibe/.hermes
ENV HERMES_INSTALL_DIR=/usr/local/lib/hermes-agent
ENV HERMES_PYTHON=/usr/local/lib/hermes-agent/venv/bin/python
ENV HERMES_WEB_DIST=/usr/local/lib/hermes-agent/hermes_cli/web_dist
ENV HERMES_TUI_DIR=/usr/local/lib/hermes-agent/ui-tui
ENV HERMES_LAZY_INSTALL_TARGET=/home/vibe/.hermes/lazy-packages
ENV PYTHONDONTWRITEBYTECODE=1
ENV PATH=/usr/local/lib/hermes-agent/venv/bin:$PATH

COPY --chown=vibe:vibe configs/builtin-skill-sync.sh /usr/local/share/cetirun/builtin-skill-sync.sh
COPY --chown=vibe:vibe agents/hermes/bootstrap/hermes-entrypoint.sh /usr/local/bin/hermes-entrypoint.sh
COPY --chown=vibe:vibe skills/connected-services/ /home/vibe/.hermes/skills/connected-services/
RUN chmod 755 /usr/local/bin/hermes-entrypoint.sh \
    /usr/local/share/cetirun/builtin-skill-sync.sh

EXPOSE 8642 7681 9119

ENTRYPOINT ["/usr/local/bin/hermes-entrypoint.sh"]
