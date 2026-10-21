# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG VERSION=dev
ARG GIT_SHA=unknown

# --- Build frontend ---
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:7292f2bcf52c3916758f1a45aba6e4dcb93f466027d7786900ea96c2d23c48e2 AS frontend
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
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:1715f34950d45c512aeaf0e571c02764f1f3f399e090ec89755ba24e1f3c2d7c AS backend
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
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:431ae1dc2924c41ae54c97c25676990f0c61d6295f42f5d07783c6976f58f1ef
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
