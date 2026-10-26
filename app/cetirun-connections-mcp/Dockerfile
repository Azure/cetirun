# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:c1fd7cad39f3f1e669cab9f9905bc0abbc3557c338065f43daa49940f6b99559 AS builder
ARG VERSION=dev
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/cetirun-connections-mcp/ ./app/cetirun-connections-mcp/
WORKDIR /src/app/cetirun-connections-mcp
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-connections-mcp/internal/mcp.Version=${VERSION}" \
    -o /cetirun-connections-mcp .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:6b85a86416a29cdcfd404e9fbd9c5ef49cc0612882cecc702535dcac39c9446c
COPY --from=builder /cetirun-connections-mcp /cetirun-connections-mcp
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/cetirun-connections-mcp"]
