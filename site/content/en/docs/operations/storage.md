---
title: "Storage"
description: "Configure Azure Disk home volumes and shared Azure Blob skills storage."
weight: 20
---

CetiRun uses persistent volumes for instance workspaces and separate read-only
volumes for shared skills.

## Two storage purposes

An instance’s `/home/vibe` holds private workspace state. The operator,
standalone runtime chart, and opt-in Custom provider all default to a `100Gi`
`ReadWriteOnce` home claim using the StorageClass named `default`. On AKS,
this class uses Azure Disk. An empty `persistence.storageClass` instead lets
Kubernetes choose the cluster's default class, which can have a different name.
The namespace is `cetirun-<metadata.name>`, not necessarily the owner's alias.

Sources: [controller values]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[runtime values]({{< repo "app/cetirun-chart/values.yaml" >}}), and
[StatefulSet storage]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}).

Shared skills use a separate static PV/PVC, mounted read-only by the agent,
when `skills.shared` is enabled or a team supplies `skillStorage`.
Their defaults remain `azureblob-nfs-premium`, `ReadWriteMany`, and `100Gi`;
`skills.shared` is disabled by default.

## Home provisioning

Hermes, OpenClaw, and Custom instances create HOME through the runtime chart's
StatefulSet `volumeClaimTemplates`. A single Helm installation submits the
StatefulSet, whose controller creates the PVC and Pod.

The operator can prepare the release before the PVC exists. For
`WaitForFirstConsumer`, scheduling the Agent Pod triggers volume binding.
Ready requires a Bound HOME and a ready workload. Custom checks the namespace,
claim name, Agent ownership and storage specification, not the PVC UID. It
requires an approved claim template; a pre-created HOME PVC is not an
alternative provisioning path.

The default changes do not migrate or resize existing claims. Preserve explicit
storage settings for existing built-in instances when upgrading. Custom HOME
must match the configured storage profile and the instance's recorded claim
template. Changing an existing claim's storage class or size still needs a
separate approved migration or expansion.

## Provision shared Blob NFS

[infra/storage]({{< repo "infra/storage" "tree" >}}) defines a Premium BlockBlobStorage
account and a private container (default `aks-skills`). Its top-level Bicep
accepts `location`, `storageAccountName`, `containerName`, `tags`, and
`allowedSubnetIds`. This account serves shared artifacts; provisioning it does
not create the instance-home StorageClass or mount a volume.

Prerequisites:

- Azure CLI with Bicep and authorization for the selected resource group.
- An existing resource group; the storage helper does not create one.
- AKS with Blob CSI enabled.
- A known node subnet or permission to discover it.
- A reviewed `PARAM_FILE`; the default `main.bicepparam` is absent from this
  repository. `location` and `storageAccountName` have no defaults.

**Offline syntax check**, using an already-installed Bicep executable:

```bash
bicep build infra/storage/main.bicep --stdout > /dev/null
```

This does not check Azure access or whether nodes can mount NFS.

**Connected, mutating:** the helper discovers the subnet from the node pool or
node resource group unless `AKS_SUBNET_ID` is set. It then updates the subnet’s
service endpoints, validates and creates the deployment, and queries outputs.

```bash
RESOURCE_GROUP=example-rg AKS_CLUSTER_NAME=example-cluster \
PARAM_FILE=/absolute/path/storage.bicepparam \
  bash infra/storage/deploy.sh
```

`RESOURCE_GROUP` and `AKS_CLUSTER_NAME` are required even when a subnet is
provided. The helper uses the current Azure subscription and requires tools
including GNU-compatible `grep -P`. Review the subnet update carefully: it supplies
`Microsoft.Storage` to `--service-endpoints`; do not assume unrelated endpoint
configuration will be preserved. It also overrides `allowedSubnetIds` with
the **single** resolved subnet, replacing any list in the parameter file.
Multi-subnet access needs a separately reviewed deployment.

The subnet write occurs before Bicep validation. Final verification includes
`az storage container show --auth-mode login`, which needs Blob data-plane
authorization and network access from the workstation. That call can fail
after provisioning succeeded, and its success does not prove an NFS mount.

## NFS security properties

The [storage module]({{< repo "infra/storage/modules/storage-account.bicep" >}})
sets hierarchical namespace and NFS v3 at account creation, disables
HTTPS-only enforcement for NFS traffic, disallows anonymous blob access, and
uses `networkAcls.defaultAction: Deny` with explicitly allowed subnets.
The SKU is `Premium_LRS`, and `networkAcls.bypass` is `AzureServices`.
The module does not create a private endpoint; a container with
`publicAccess: None` is not the same as private-endpoint-only networking.

NFS traffic is not protected by HTTPS, and network access is central to this
mount path. Do not describe Azure AD/RBAC or account-key authentication as the
NFS data-plane authentication mechanism. Data ownership, mount permissions,
network isolation, and backups need separate review.

## Shared skills mounts

Configure `skills.resourceGroup`, `skills.storageAccount`, and
`skills.containerName`, or provide the equivalent `Team.skillStorage`.
The default mount path is `/home/vibe/.aks/skills`.

NFS is the default. `skills.protocol` also accepts `fuse` and `fuse2` for
separately configured Blobfuse mounts. Those modes require `skills.secretName`
to reference an existing staging Secret in **each instance namespace**; the
chart does not create that Secret. The team storage fields do not select the
protocol or Secret: use reviewed Helm values for those settings.

The [static PV template]({{< repo "app/cetirun-chart/templates/shared-skills-pv.yaml" >}})
creates a PV/PVC pair per release, not a cross-namespace PVC. Each pair can
point at the same container; the runtime mounts it read-only. Protocol,
Secret reference, and mount options are install-time settings for this chart,
not a live protocol-switch workflow. Plan replacement PV/PVC resources or a
replacement instance before retiring existing storage.

Shared mounts and npm-installed skills are separate paths. The
[skill installer]({{< repo "app/cetirun-chart/configs/init-skills.sh" >}})
writes npm skills into `/home/vibe/.cetirun/skills/npm`, prunes entries no longer
requested, and fails initialization when any requested install fails. Set
`skills.npmRegistry` explicitly for your approved reachable registry; the
default feed is environment-specific. `skills.clawHub` entries are not installed.

## Retention is not deletion protection

With the default StatefulSet claim templates, the value
`persistence.retainPolicy: Retain` retains PVCs when the StatefulSet is deleted
or scaled down. The template emits that retention field only for a rendered
Kubernetes version of at least 1.25. This setting does not override a PV's
reclaim policy or protect a PVC from namespace deletion.
The AKS `default` StorageClass normally uses a `Delete` reclaim policy, so
deleting a HOME PVC can also delete its Azure Disk. Check the actual PV policy
and arrange a backup before deleting an instance.

The [instance deletion handler]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
uninstalls Helm and deletes the entire instance namespace, including PVCs.
The PV and underlying data then follow the actual reclaim policy and CSI
behavior. Suspension normally retains the home, but a reviewed
`persistence.retainPolicy: Delete` override can delete claims on scale-down.
Do not use suspension as a backup.

## Recovery checks

Check PVC binding, the selected StorageClass and CSI driver, subnet access, and
mount events before changing the workload. Keep sensitive file listings out of
public diagnostics. Test restore procedures; neither pod persistence nor a
`Retain` policy is a substitute for backup.

The runtime's [pre-upgrade/pre-rollback hook]({{< repo "app/cetirun-chart/templates/pre-upgrade-hook.yaml" >}})
can orphan-delete and recreate a StatefulSet when immutable fields differ.
It does not migrate data or automatically resize an existing PVC. A successful
chart update must be followed by checks of the mounted storage class, size,
and contents.

See [lifecycle](../../guides/lifecycle/) before deleting an instance or namespace.
