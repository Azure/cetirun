# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:3607c431963c588d69b3be0c31cea63abd197a324bbeede998dda702597d8368 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_SHA=unknown
WORKDIR /workspace
COPY app/cetirun-memory-context/ .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-X main.version=${VERSION} -X main.commit=${GIT_SHA}" -o /cetirun-memory ./cmd/cetirun-memory

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:e952865122598043cfb54f0257f016e28e02c21904e172b47dcb2148e095693a
ARG VERSION=dev
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=builder /cetirun-memory /cetirun-memory
USER 1024:1024
ENTRYPOINT ["/cetirun-memory"]
