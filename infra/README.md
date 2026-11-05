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
| `public-ingress/` | Optional public DNS, cert-manager HTTP-01, and Gateway API preparation |

Deployment scripts change cloud resources and can change the local Kubernetes
configuration. Supply reviewed parameters that are specific to the environment.
Default parameter files and registry access are possibly not available.

The public-ingress helper prepares one HTTPS hostname for an existing managed
Istio ingress. It is separate from the cluster template and from `make deploy`.
Read [public ingress operations](../site/content/en/docs/operations/public-ingress.md)
before using it.
