# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:1715f34950d45c512aeaf0e571c02764f1f3f399e090ec89755ba24e1f3c2d7c AS builder
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/cetirun-operator/go.mod ./app/cetirun-operator/go.mod
COPY app/cetirun-agent-broker/ ./app/cetirun-agent-broker/
WORKDIR /src/app/cetirun-agent-broker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /agent-broker .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:431ae1dc2924c41ae54c97c25676990f0c61d6295f42f5d07783c6976f58f1ef
COPY --from=builder /agent-broker /agent-broker
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/agent-broker"]
