# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG VERSION=dev
ARG GIT_SHA=unknown

# --- Build frontend ---
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:bfb630450e9c3d7428d59f25ce6df95ba5634d664d96b06017aa56f87da5ec0e AS frontend
ARG NPM_REGISTRY=https://registry.npmjs.org/
RUN tdnf install -y ca-certificates nodejs24 nodejs24-npm && tdnf clean all
WORKDIR /app/web
COPY app/cetirun-portal/web/package.json app/cetirun-portal/web/package-lock.json ./
RUN npm config set registry "${NPM_REGISTRY}" && \
    npm config set replace-registry-host npmjs && \
    npm ci --legacy-peer-deps
COPY app/cetirun-portal/web/ ./
RUN npm run build

# --- Build backend ---
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:2b3c026371fe2221a588f80cfb5dbc592af1efd3be0d53297676c7f3a612a5d9 AS backend
# Use the published Microsoft patch until its MCR image is available.
# Pins: https://github.com/microsoft/go/releases/tag/v1.27.4-1
ENV GOLANG_VERSION=1.27.4 GOTOOLCHAIN=local
RUN set -eu; \
    case "$(uname -m)" in \
      x86_64) go_url="https://download.visualstudio.microsoft.com/download/pr/b32709ee-0545-4d3f-913f-bf51b626be52/33fbba165da669d1cb19d3799e623805/go1.27.4-20261110.2.linux-amd64.tar.gz"; go_sha256=423eee29e71d3da6b8d59f7fa080ca21f28015857af58f3da5349c264a5f3f2f ;; \
      aarch64) go_url="https://download.visualstudio.microsoft.com/download/pr/b32709ee-0545-4d3f-913f-bf51b626be52/a5caf67eaca4942d973c08bb1e54fa40/go1.27.4-20261110.2.linux-arm64.tar.gz"; go_sha256=8bd381cdc3a3dc932117a3c32ac49ac8787d8a32ca4fba13ecbac890265afd3a ;; \
      *) echo "Unsupported Go build architecture" >&2; exit 1 ;; \
    esac; \
    curl -fsSL --connect-timeout 15 --max-time 300 --retry 3 --retry-delay 5 "$go_url" -o /tmp/go.tar.gz; \
    echo "$go_sha256  /tmp/go.tar.gz" | sha256sum --check; \
    rm -rf /usr/local/go; \
    tar xzf /tmp/go.tar.gz -C /usr/local; \
    rm /tmp/go.tar.gz; \
    test "$(go env GOVERSION)" = go1.27.4
ARG VERSION
ARG GIT_SHA
ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace
# Copy local modules needed for API types and internal contracts.
COPY app/cetirun-operator/ app/cetirun-operator/
COPY app/internal/ app/internal/
# Copy portal module.
COPY app/cetirun-portal/go.mod app/cetirun-portal/go.sum app/cetirun-portal/
WORKDIR /workspace/app/cetirun-portal
RUN go mod download
COPY app/cetirun-portal/ ./
# Copy frontend build into the location expected by embed.FS.
COPY --from=frontend /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-portal/internal/buildinfo.Version=${VERSION} -X github.com/Azure/CetiRun/app/cetirun-portal/internal/buildinfo.Commit=${GIT_SHA}" \
    -o /portal .

# --- Runtime ---
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:370fb2c55e7f0474fed019623cce6a8db65a4d70dedfca9219fc69d6c7627f89
ARG VERSION
ARG GIT_SHA

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${GIT_SHA}"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=backend /portal /portal
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/portal"]
