# app/

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

This directory contains its deployable application components.

Start with the [site architecture guide](../site/content/en/docs/concepts/architecture.md)
or [Install CetiRun](../site/content/en/docs/getting-started/platform.md).

| Directory | Description |
|-----------|-------------|
| `cetirun-platform/` | Umbrella chart for installing the operator and portal together. |
| `cetirun-operator/` | Kubernetes operator for managing Agent and Team CRDs. Contains Go source code and its own Helm chart for operator deployment. |
| `cetirun-chart/` | Per-instance agent Helm chart. Contains templates, values, configs, and named Dockerfiles for OpenClaw, Hermes, shared base, and desktop images. |
| `cetirun-portal/` | Self-service portal for creating, managing, and monitoring runtime instances. Contains a Go backend and React frontend. |
| `cetirun-ai-gateway/` | AI Gateway: the `cetirun-ai-gateway` image, open-source LiteLLM on Azure Linux, and its Helm chart, which installs standalone with bundled PostgreSQL and Valkey. |
| `adminclaw/` | Administrative deployment tools and platform operations skills. |
