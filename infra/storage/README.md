# CetiRun Blob storage

Read [Storage](../../site/content/en/docs/operations/storage.md)
for prerequisites, required parameters, helper side effects, NFS network
restrictions, shared skills mounts, and recovery.

`main.bicep` calls `modules/storage-account.bicep`. `deploy.sh` discovers or
accepts an AKS subnet, updates its storage service endpoint, and deploys the
account/container. Supply a reviewed `PARAM_FILE`; a default
`main.bicepparam` is not included.
