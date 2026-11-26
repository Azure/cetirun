# CetiRun skill storage

Read [Storage](../../site/content/en/docs/operations/storage.md)
for prerequisites, necessary parameters, helper side effects, NFS network
restrictions, shared skills mounts, and recovery.

`main.bicep` calls `modules/storage-account.bicep`. The default `blob` backend
retains the existing Premium Blob/NFS behavior. For production, set
`STORAGE_BACKEND=azure-files-nfs`: this creates a Premium `FileStorage`
account, an NFS 4.1 share with an explicit quota, and enables the Azure Files
service-side encryption-in-transit requirement. The secure backend requires
`PUBLIC_NETWORK_ACCESS=Disabled`, a private endpoint subnet, and a
`privatelink.file.core.windows.net` private DNS zone ID. It never adds an
account-key credential or a policy exemption. The Bicep module also forces
public network access to `Disabled` for this backend, even when called directly
without `deploy.sh`.

The share is created with `RootSquash` so UID 0 cannot write as root. Azure
Files NFS uses POSIX ownership and permissions; before enabling Marketplace
writes, perform an approved bootstrap mount and verify that the intended
non-root worker UID/GID (currently 65532) owns or can write the required
directories. Do not weaken root squash as a shortcut; if ownership is not
correct, stop deployment and use an approved non-root bootstrap procedure.

`deploy.sh` validates the explicit subscription and tenant before any Azure
write. Blob mode discovers or accepts an AKS subnet and enables the
`Microsoft.Storage` service endpoint when no private endpoint is used. Azure
Files mode deliberately skips that subnet mutation and requires the private
endpoint/DNS inputs. Supply a reviewed `PARAM_FILE`; a default
`main.bicepparam` is not included. The deployment environment variables
`STORAGE_BACKEND`, `SHARE_NAME`, `SHARE_QUOTA_GIB`,
`PRIVATE_ENDPOINT_SUBNET_ID`, `PRIVATE_DNS_ZONE_ID`, and
`PUBLIC_NETWORK_ACCESS` are explicit deployment overrides and take precedence
over values in `PARAM_FILE`.

For example, after reviewing the target IDs and DNS zone:

```bash
STORAGE_BACKEND=azure-files-nfs \
PUBLIC_NETWORK_ACCESS=Disabled \
PRIVATE_ENDPOINT_SUBNET_ID="/subscriptions/<subscription-id>/resourceGroups/example-network-rg/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/example-nodes" \
PRIVATE_DNS_ZONE_ID="/subscriptions/<dns-subscription-id>/resourceGroups/example-dns-rg/providers/Microsoft.Network/privateDnsZones/privatelink.file.core.windows.net" \
SHARE_NAME=aks-skills SHARE_QUOTA_GIB=100 \
RESOURCE_GROUP=example-rg AKS_CLUSTER_NAME=example-cluster \
AZURE_SUBSCRIPTION_ID="<subscription-id>" AZURE_TENANT_ID="<tenant-id>" \
PARAM_FILE=/absolute/path/storage.bicepparam bash infra/storage/deploy.sh
```
