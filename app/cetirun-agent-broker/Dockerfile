# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.4-1@sha256:b11f7596b22e3e95d0105c8bbd9e173e21a9f812e112713971330bc21d0248b4 AS builder
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/cetirun-operator/go.mod ./app/cetirun-operator/go.mod
COPY app/cetirun-agent-broker/ ./app/cetirun-agent-broker/
WORKDIR /src/app/cetirun-agent-broker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /agent-broker .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:11ecd8f7cd1cad0e4fbb565ab98768f1a2ce35cb2730c867217baac011ae8645
COPY --from=builder /agent-broker /agent-broker
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/agent-broker"]
