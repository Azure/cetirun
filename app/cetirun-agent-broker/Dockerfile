# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:c1fd7cad39f3f1e669cab9f9905bc0abbc3557c338065f43daa49940f6b99559 AS builder
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
