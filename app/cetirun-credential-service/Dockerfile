# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.8-1@sha256:5ef73864bf3795d1a5514e59b388028ad80190d7ba48f62f872328db9689e38d AS builder
WORKDIR /src
COPY app/cetirun-credential-service/go.mod ./
COPY app/cetirun-credential-service/ ./
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=linux go build -trimpath -o /credential-service .

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:4377af4aa7a810b7d59f691eae5066895a71aa3eee4cfb4eba527bbebff16479
COPY --from=builder /credential-service /credential-service
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/credential-service"]
