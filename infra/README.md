# infra/

Cluster-level infrastructure configuration for CetiRun.

| Directory     | Description                                                                               |
| ------------- | ----------------------------------------------------------------------------------------- |
| `cluster/`    | AKS cluster provisioning and node pool configuration (Bicep/Terraform)                    |
| `istio/`      | Istio-based authentication infrastructure: OAuth2 Proxy, ext_authz, EnvoyFilters, Gateway |
| `storage/`    | Azure Blob NFS Premium StorageClass definitions                                           |
| `monitoring/` | Prometheus + Grafana or Azure Monitor stack configuration                                 |

## Related TODO Items

- #1: Provision AKS cluster with system node pool
- #2: Configure openclaw and shared node pools with taints and autoscaling
- #3: Design and implement secrets management
- #4: Deploy Istio Gateway with host-based routing and WebSocket support
- #5: Configure Azure Blob NFS Premium StorageClass
- #9: Deploy monitoring stack
- #10: Configure external API access for skills
