# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:1715f34950d45c512aeaf0e571c02764f1f3f399e090ec89755ba24e1f3c2d7c AS builder
ARG VERSION=dev
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/cetirun-connections-mcp/ ./app/cetirun-connections-mcp/
WORKDIR /src/app/cetirun-connections-mcp
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-connections-mcp/internal/mcp.Version=${VERSION}" \
    -o /cetirun-connections-mcp .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:431ae1dc2924c41ae54c97c25676990f0c61d6295f42f5d07783c6976f58f1ef
COPY --from=builder /cetirun-connections-mcp /cetirun-connections-mcp
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/cetirun-connections-mcp"]
