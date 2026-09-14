# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Build stage
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.8-1@sha256:1c312e239157abd92e3eb83091ac5febb914b57eb117af7faa46659109632bdd AS builder

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
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:f8f5a9bb739ad1ec347853144c9ed4ca2260e587082277bc6066fcd5cc9973e8

WORKDIR /

# Copy the compiled binary.
COPY --from=builder /workspace/manager /manager

# Chart is now pulled from OCI registry at runtime.
# No bundled chart in the image.

USER 65532:65532

ENTRYPOINT ["/manager"]
