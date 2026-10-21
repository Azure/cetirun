# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.2-1@sha256:c5e8b4403616d5d4e85a87bf7d2be4f117798e1fac2bbf9734ea0db1a226412b AS builder
ARG VERSION=dev
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/agent-tools-mcp/ ./app/agent-tools-mcp/
WORKDIR /src/app/agent-tools-mcp
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/Azure/CetiRun/app/agent-tools-mcp/internal/mcp.Version=${VERSION}" \
    -o /agent-tools-mcp .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:431ae1dc2924c41ae54c97c25676990f0c61d6295f42f5d07783c6976f58f1ef
COPY --from=builder /agent-tools-mcp /agent-tools-mcp
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/agent-tools-mcp"]
