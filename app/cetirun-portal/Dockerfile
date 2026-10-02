# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG VERSION=dev
ARG GIT_SHA=unknown

# --- Build frontend ---
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:1324a2cf7ed34e5f48a1022816b205782b86c7305651658e611dcd3d30756751 AS frontend
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
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.26.8-1@sha256:4e652d1254a73a25bbc34dfda45dfca5dfae927f7245cc914310ff1057580499 AS backend
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
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-portal/internal/buildinfo.Version=${VERSION} -X github.com/Azure/CetiRun/app/cetirun-portal/internal/buildinfo.Commit=${GIT_SHA}" \
    -o /portal .

# --- Runtime ---
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:2b5cec59b51cb0509157e3a1b730e19aa42912b99d7f5300b87eac7000bfea19
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
