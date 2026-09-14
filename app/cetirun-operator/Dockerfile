# Build stage
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.5@sha256:ea712a1aaf80306c19ff842ba0bfcb9ad360afd8143e70044e0d0bd6d6899887 AS builder

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
