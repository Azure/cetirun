# --- Build frontend ---
FROM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:1c56f09437dfc2910faad39abaaed336265d246cf183e2adb362d4cb3b881ab6 AS frontend
ARG NPM_REGISTRY=https://packagefeedproxy.microsoft.io/npm/
RUN tdnf install -y ca-certificates nodejs24 nodejs24-npm && tdnf clean all
WORKDIR /app/web
COPY app/cetirun-portal/web/package.json app/cetirun-portal/web/package-lock.json ./
RUN npm config set registry "${NPM_REGISTRY}" && \
    npm config set replace-registry-host npmjs && \
    npm ci --legacy-peer-deps
COPY app/cetirun-portal/web/ ./
RUN npm run build

# --- Build backend ---
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.5@sha256:ea712a1aaf80306c19ff842ba0bfcb9ad360afd8143e70044e0d0bd6d6899887 AS backend
WORKDIR /workspace
# Copy operator module (needed for CRD types via replace directive).
COPY app/cetirun-operator/ app/cetirun-operator/
# Copy portal module.
COPY app/cetirun-portal/go.mod app/cetirun-portal/go.sum app/cetirun-portal/
WORKDIR /workspace/app/cetirun-portal
RUN go mod download
COPY app/cetirun-portal/ ./
# Copy frontend build into the location expected by embed.FS.
COPY --from=frontend /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=linux go build -o /portal .

# --- Runtime ---
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:f8f5a9bb739ad1ec347853144c9ed4ca2260e587082277bc6066fcd5cc9973e8
COPY --from=backend /portal /portal
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/portal"]
