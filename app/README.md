# app/

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

This directory contains its deployable application components.

Start with the [site architecture guide](../site/content/en/docs/concepts/architecture.md)
or [Install CetiRun](../site/content/en/docs/getting-started/platform.md).

| Directory | Description |
|-----------|-------------|
| `cetirun-platform/` | Umbrella chart that installs the operator, the Portal, the AI Gateway, Connections MCP, and the observability chart together. |
| `cetirun-operator/` | Kubernetes operator for the Agent, Team, Sandbox, and AgentPolicy CRDs, Sandbox admission, and the Hermes-based [CetiAdmin](../site/content/en/docs/operations/cetiadmin.md), whose source is in `cetirun-operator/internal/cetiadmin`. Includes the operator Helm chart, operations skills, and helpers. |
| `cetirun-chart/` | Agent chart, with one Helm release for each Agent. It contains templates, values, configs, and named Dockerfiles for OpenClaw, Hermes, shared base, and desktop images. |
| `cetirun-portal/` | Self-service Portal to create, manage, and monitor Agents. It contains a Go backend and a React frontend. |
| `cetirun-ai-gateway/` | AI Gateway: the `cetirun-ai-gateway` image, open-source LiteLLM on Azure Linux, and its Helm chart, which installs standalone with bundled PostgreSQL and Valkey. |
| `cetirun-agent-broker/` | Agent Identity Broker. It keeps the encrypted credentials of Agent connections and gives Connections MCP short-lived tokens. |
| `cetirun-connections-mcp/` | MCP server that Agents call to use their connections. It calls the external services with short-lived tokens from the Broker. |
| `cetirun-memory-context/` | The built-in `cetirun-memory` skill, its helper, and the image that installs them into Agents. |
| `cetirun-observability/` | Chart that collects the logs, Kubernetes events, and metrics of the platform and the Agents into Azure Data Explorer with ADX-Mon. `make deploy` installs it with the platform. |
| `internal/` | Go packages that more than one component uses. |
