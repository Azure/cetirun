# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:2b3c026371fe2221a588f80cfb5dbc592af1efd3be0d53297676c7f3a612a5d9 AS builder
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
