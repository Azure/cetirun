# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:7a193a3f8ee2ffc8c1420affd7e13cfee4e5d1291a54b1dee370ee1ea3b5241a AS builder
ARG VERSION=dev
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/cetirun-connections-mcp/ ./app/cetirun-connections-mcp/
WORKDIR /src/app/cetirun-connections-mcp
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-connections-mcp/internal/mcp.Version=${VERSION}" \
    -o /cetirun-connections-mcp .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:e952865122598043cfb54f0257f016e28e02c21904e172b47dcb2148e095693a
COPY --from=builder /cetirun-connections-mcp /cetirun-connections-mcp
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/cetirun-connections-mcp"]
