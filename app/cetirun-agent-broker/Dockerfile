# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.8-1@sha256:ece502dc873c17e7ba99f8524b55754adb7f14b24a2a247130649b2cb1c985c3 AS builder
WORKDIR /src
COPY app/cetirun-agent-broker/go.mod ./
COPY app/cetirun-agent-broker/ ./
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=linux go build -trimpath -o /agent-broker .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:2b5cec59b51cb0509157e3a1b730e19aa42912b99d7f5300b87eac7000bfea19
COPY --from=builder /agent-broker /agent-broker
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/agent-broker"]
