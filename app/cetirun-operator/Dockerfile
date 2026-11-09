# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG VERSION=dev
ARG GIT_SHA=unknown

# Package the target Helm CLI using the shared tool lock.
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:bd88359666e3f20296e7fe639a480dfd3c3c8eba299b21f26e4560353e21a656 AS cetiadmin-tools
ARG TARGETARCH
RUN tdnf install -y ca-certificates python3 && tdnf clean all
COPY scripts/ci/install_tools.py scripts/ci/tools.json /tools/
RUN python3 /tools/install_tools.py --arch "${TARGETARCH}" --bin-dir /out/bin helm

# Build stage
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:7a193a3f8ee2ffc8c1420affd7e13cfee4e5d1291a54b1dee370ee1ea3b5241a AS builder
ARG VERSION
ARG GIT_SHA
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace/app/cetirun-operator

# Copy go module files and download dependencies first (layer caching).
COPY app/cetirun-operator/go.mod app/cetirun-operator/go.sum ./
COPY app/internal/go.mod app/internal/go.sum ../internal/
RUN go mod download

# Copy operator source.
COPY app/cetirun-operator/ .
COPY app/internal/ ../internal/

# Build the manager binary, and the Skill Marketplace worker that the chart
# runs in a separate Pod.
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -a \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-operator/internal/buildinfo.Version=${VERSION} -X github.com/Azure/CetiRun/app/cetirun-operator/internal/buildinfo.Commit=${GIT_SHA}" \
    -o /manager ./cmd/main.go
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-operator/internal/buildinfo.Version=${VERSION} -X github.com/Azure/CetiRun/app/cetirun-operator/internal/buildinfo.Commit=${GIT_SHA}" \
    -o /skill-marketplace ./cmd/skill-marketplace

# ──────────────────────────────────────────────
# Runtime stage
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:370fb2c55e7f0474fed019623cce6a8db65a4d70dedfca9219fc69d6c7627f89
ARG VERSION
ARG GIT_SHA

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${GIT_SHA}"
LABEL org.opencontainers.image.licenses="MIT"

WORKDIR /

# Copy the compiled binaries.
COPY --from=builder /manager /manager
COPY --from=builder /skill-marketplace /skill-marketplace
COPY --from=cetiadmin-tools --chmod=0755 /out/bin/helm /usr/local/share/cetirun/cetiadmin/bin/helm

# Chart is now pulled from OCI registry at runtime.
# No bundled chart in the image.

USER 65532:65532

ENTRYPOINT ["/manager"]
