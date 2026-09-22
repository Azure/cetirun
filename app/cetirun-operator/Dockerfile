# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Build stage
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.8-1@sha256:5ef73864bf3795d1a5514e59b388028ad80190d7ba48f62f872328db9689e38d AS builder

WORKDIR /workspace

# Copy go module files and download dependencies first (layer caching).
COPY app/cetirun-operator/go.mod app/cetirun-operator/go.sum ./
RUN go mod download

# Copy operator source.
COPY app/cetirun-operator/ .

# Build the manager binary.
RUN GOOS=linux go build -a -o manager ./cmd/main.go

# ──────────────────────────────────────────────
# Runtime stage
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:4377af4aa7a810b7d59f691eae5066895a71aa3eee4cfb4eba527bbebff16479

WORKDIR /

# Copy the compiled binary.
COPY --from=builder /workspace/manager /manager

# Chart is now pulled from OCI registry at runtime.
# No bundled chart in the image.

USER 65532:65532

ENTRYPOINT ["/manager"]
