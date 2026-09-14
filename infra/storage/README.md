# AKS Skills Blob Storage (NFS)

Premium Blob Storage account with an `aks-skills` container, exposed to AKS via the Blob CSI driver over NFS v3.

## Files

| File | Purpose |
|------|---------|
| `main.bicep` | Orchestrator — params + module call + outputs |
| `main.bicepparam` | Parameter overrides |
| `modules/storage-account.bicep` | Storage account + container resources |
| `deploy.sh` | Idempotent deployment script |

## Prerequisites

- Azure CLI with Bicep (`az bicep install`)
- An AKS cluster with the **Azure Blob CSI driver** enabled
- `kubectl` configured to target the cluster
- The AKS node subnet must have `Microsoft.Storage` service endpoint enabled

## Deploy

### 1. Provision the storage account

```bash
./infra/storage/deploy.sh
```

The script auto-detects the AKS node subnet ID from the cluster.

Environment variable overrides:

| Variable | Default | Description |
|----------|---------|-------------|
| `RESOURCE_GROUP` | *(required)* | Azure resource group |
| `AKS_CLUSTER_NAME` | *(required)* | AKS cluster name (for subnet auto-detection) |
| `AKS_SUBNET_ID` | *(auto-detected)* | AKS node subnet resource ID (skips auto-detection) |

## Notes

- **NFS v3 enabled** — `isHnsEnabled` and `isNfsV3Enabled` are set at account creation (cannot be changed later).
- **`supportsHttpsTrafficOnly: false`** — required because NFS v3 uses non-encrypted TCP.
- **Public network access allowed** — authentication still required (Azure AD/RBAC or account key). Lock down with `defaultAction: 'Deny'` when ready.

