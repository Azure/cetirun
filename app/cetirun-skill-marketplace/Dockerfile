# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

ARG VERSION=dev
ARG GIT_SHA=unknown

FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:3607c431963c588d69b3be0c31cea63abd197a324bbeede998dda702597d8368 AS build
ARG VERSION
ARG GIT_SHA
ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace
# Local modules that provide the shared API types.
COPY app/cetirun-operator/ app/cetirun-operator/
COPY app/internal/ app/internal/
COPY app/cetirun-skill-marketplace/go.mod app/cetirun-skill-marketplace/go.sum app/cetirun-skill-marketplace/
WORKDIR /workspace/app/cetirun-skill-marketplace
RUN go mod download
COPY app/cetirun-skill-marketplace/ ./
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${GIT_SHA}" \
    -o /cetirun-skill-marketplace .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:e952865122598043cfb54f0257f016e28e02c21904e172b47dcb2148e095693a
ARG VERSION
ARG GIT_SHA

LABEL org.opencontainers.image.source="https://github.com/Azure/CetiRun"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${GIT_SHA}"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=build /cetirun-skill-marketplace /cetirun-skill-marketplace
USER 65532:65532
ENTRYPOINT ["/cetirun-skill-marketplace"]
