# CetiRun infrastructure

Before you deploy, read
[Infrastructure](../site/content/en/docs/operations/infrastructure.md) and the
[platform checklist](../site/content/en/docs/getting-started/platform.md).

[Agent Identity setup](../site/content/en/docs/operations/agent-identity.md)
gives information about the other resources that are necessary for Agent
connections: Key Vault, workload federation, certificates, and provider
registrations. The infrastructure templates do not create these resources.

| Directory | Purpose |
| --- | --- |
| `cluster/` | AKS Bicep, node pools, identity, CSI, and monitoring configuration |
| `storage/` | Shared Azure Blob NFS Bicep and subnet-aware deployment helper |
| `istio/` | Shared authentication, routing, security headers, and egress policy |

Deployment scripts change cloud resources and can change the local Kubernetes
configuration. Supply reviewed parameters that are specific to the environment.
Default parameter files and registry access are possibly not available.
