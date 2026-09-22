# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# --- Build frontend ---
FROM mcr.microsoft.com/azurelinux/base/core:3.0@sha256:34a22db497ff34a0f35ca5fc54bd38711d04238a2c1b2f65d35dc9d45dd82584 AS frontend
ARG NPM_REGISTRY=https://registry.npmjs.org/
RUN tdnf install -y ca-certificates nodejs24 nodejs24-npm && tdnf clean all
WORKDIR /app/web
COPY app/cetirun-portal/web/package.json app/cetirun-portal/web/package-lock.json ./
RUN npm config set registry "${NPM_REGISTRY}" && \
    npm config set replace-registry-host npmjs && \
    npm ci --legacy-peer-deps
COPY app/cetirun-portal/web/ ./
RUN npm run build

# --- Build backend ---
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.8-1@sha256:5ef73864bf3795d1a5514e59b388028ad80190d7ba48f62f872328db9689e38d AS backend
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
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0@sha256:4377af4aa7a810b7d59f691eae5066895a71aa3eee4cfb4eba527bbebff16479
COPY --from=backend /portal /portal
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/portal"]
