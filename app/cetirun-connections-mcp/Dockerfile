# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.4-1@sha256:beab7184dddd56cb4fb652a2e9affb25ce377d85f30a7c14eed0e65a7b315c4e AS builder
ARG VERSION=dev
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/cetirun-connections-mcp/ ./app/cetirun-connections-mcp/
WORKDIR /src/app/cetirun-connections-mcp
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/Azure/CetiRun/app/cetirun-connections-mcp/internal/mcp.Version=${VERSION}" \
    -o /cetirun-connections-mcp .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:370fb2c55e7f0474fed019623cce6a8db65a4d70dedfca9219fc69d6c7627f89
COPY --from=builder /cetirun-connections-mcp /cetirun-connections-mcp
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/cetirun-connections-mcp"]
