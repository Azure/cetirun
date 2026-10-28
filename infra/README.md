# CetiRun infrastructure

Read [Infrastructure](../site/content/en/docs/operations/infrastructure.md)
and [platform checklist](../site/content/en/docs/getting-started/platform.md)
before deploying.

[Agent Identity setup](../site/content/en/docs/operations/agent-identity.md)
covers the additional Key Vault, workload federation, certificates and provider
registrations needed for Agent connections. The infrastructure templates do
not create these resources.

| Directory | Purpose |
| --- | --- |
| `cluster/` | AKS Bicep, node pools, identity, CSI, and monitoring configuration |
| `storage/` | Shared Azure Blob NFS Bicep and subnet-aware deployment helper |
| `istio/` | Shared authentication, routing, security headers, and egress policy |

Deployment scripts change cloud resources and can modify local Kubernetes
configuration. Supply reviewed environment-specific parameters; do not assume
default parameter files or registry access are available.
