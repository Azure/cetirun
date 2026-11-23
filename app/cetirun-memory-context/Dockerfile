# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.4-1@sha256:b11f7596b22e3e95d0105c8bbd9e173e21a9f812e112713971330bc21d0248b4 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_SHA=unknown
WORKDIR /workspace
COPY app/cetirun-memory-context/ .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-X main.version=${VERSION} -X main.commit=${GIT_SHA}" -o /cetirun-memory ./cmd/cetirun-memory

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:370fb2c55e7f0474fed019623cce6a8db65a4d70dedfca9219fc69d6c7627f89
ARG VERSION=dev
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=builder /cetirun-memory /cetirun-memory
USER 1024:1024
ENTRYPOINT ["/cetirun-memory"]
