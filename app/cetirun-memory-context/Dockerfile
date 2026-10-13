# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.2-1@sha256:7ff46ce2498a27b9f00537a945d5d6cbad7e11f695e66ba5c922038387944f40 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_SHA=unknown
WORKDIR /workspace
COPY app/cetirun-memory-context/ .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-X main.version=${VERSION} -X main.commit=${GIT_SHA}" -o /cetirun-memory ./cmd/cetirun-memory

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:431ae1dc2924c41ae54c97c25676990f0c61d6295f42f5d07783c6976f58f1ef
ARG VERSION=dev
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=builder /cetirun-memory /cetirun-memory
USER 1024:1024
ENTRYPOINT ["/cetirun-memory"]
