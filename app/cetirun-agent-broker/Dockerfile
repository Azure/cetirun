# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.2-1@sha256:91e046cbf8a47a056e5a3c59b40d5a6d40aa7ad70c8657107212ccd5b3d11029 AS builder
WORKDIR /src
COPY app/cetirun-agent-broker/go.mod ./
COPY app/cetirun-agent-broker/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /agent-broker .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:431ae1dc2924c41ae54c97c25676990f0c61d6295f42f5d07783c6976f58f1ef
COPY --from=builder /agent-broker /agent-broker
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/agent-broker"]
