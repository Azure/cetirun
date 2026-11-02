# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.3-1@sha256:3607c431963c588d69b3be0c31cea63abd197a324bbeede998dda702597d8368 AS builder
WORKDIR /src
COPY app/internal/ ./app/internal/
COPY app/cetirun-operator/go.mod ./app/cetirun-operator/go.mod
COPY app/cetirun-agent-broker/ ./app/cetirun-agent-broker/
WORKDIR /src/app/cetirun-agent-broker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /agent-broker .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:e952865122598043cfb54f0257f016e28e02c21904e172b47dcb2148e095693a
COPY --from=builder /agent-broker /agent-broker
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/agent-broker"]
