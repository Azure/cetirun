# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.2-1@sha256:7ff46ce2498a27b9f00537a945d5d6cbad7e11f695e66ba5c922038387944f40 AS builder
WORKDIR /src
COPY app/cetirun-agent-broker/go.mod ./
COPY app/cetirun-agent-broker/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /agent-broker .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:431ae1dc2924c41ae54c97c25676990f0c61d6295f42f5d07783c6976f58f1ef
COPY --from=builder /agent-broker /agent-broker
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/agent-broker"]
