---
title: "Storage"
description: "Configure Azure Disk home volumes and the shared Azure Blob storage for skills."
weight: 20
---

CetiRun uses persistent volumes for the HOME of each Agent and separate
read-only volumes for shared skills.

## Two storage purposes

The HOME of an Agent, `/home/vibe`, contains its private state. By default,
the operator, the standalone agent chart, and Custom Agents all use a `100Gi`
`ReadWriteOnce` home claim with the StorageClass named `default`. On
AKS, this class uses Azure Disk. If `persistence.storageClass` is empty,
Kubernetes selects the cluster's default class instead. That class can have a
different name. The namespace is `cetirun-<metadata.name>`, which is not always
the owner's alias.

Sources: [controller values]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[agent chart values]({{< repo "app/cetirun-chart/values.yaml" >}}), and
[StatefulSet storage]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}).

If `skills.shared` is enabled or a team supplies `skillStorage`, shared skills
use a separate static PV/PVC. The agent mounts this PV/PVC read-only. The
defaults for shared skills stay `azureblob-nfs-premium`, `ReadWriteMany`, and
`100Gi`. The agent chart disables `skills.shared` by default, but
`make deploy` enables it for all Agents. Refer to
[Shared skills storage for `make deploy`](#shared-skills-storage-for-make-deploy).

## Home provisioning

Hermes, OpenClaw, and Custom Agents create HOME through the agent chart's
StatefulSet `volumeClaimTemplates`. A single Helm installation submits the
StatefulSet. The StatefulSet controller creates the PVC and the Pod.

The operator can prepare the release before the PVC exists. For
`WaitForFirstConsumer`, volume binding starts when the Agent Pod is scheduled.
A Bound HOME and a ready workload are necessary for Ready. Custom examines the
namespace, the claim name, the Agent ownership, and the storage specification,
but not the PVC UID. For Custom, an approved claim template is necessary. A
pre-created HOME PVC is not an alternative way to provision HOME.

The changes to the default values do not migrate or resize claims that already
exist. When you upgrade, keep the explicit storage settings of built-in
Agents that already exist. Custom HOME must match the configured storage
profile and the recorded claim template of the Agent. A separate approved
migration or expansion is still necessary to change the storage class or size
of a claim that already exists.

## Sandbox HOME class selection

When native Sandbox sources are configured, Portal administrators choose HOME
StorageClasses from a list rather than typing a cluster-specific name. The list
contains the StorageClasses that `platform.json` approves. This file is in the
platform source ConfigMap that `sandboxSources.platformConfigMapName` names,
`sandbox-platform` in the examples. Refer to
[The platform registration](../policy-teams/#the-platform-registration). The
Portal verifies the referenced ConfigMap and each selected class's UID and
provisioner before showing it. A configured default is only a form preference;
it is not an approval.

The Portal does not show a stale, missing, or replaced class as a choice. It
keeps that value visible when editing an older Sandbox so that a Portal
administrator can make an explicit replacement. A failed source read or an
empty approved set prevents a save. HOME access modes are declared by the
selected runtime registration. They are not inferred from the storage driver.

This UI behavior is not a CSI qualification. Sandbox admission and the
controller continue to verify the selected class when configuration is saved
and before execution.

## Provision shared Blob NFS

[infra/storage]({{< repo "infra/storage" "tree" >}}) defines a Premium BlockBlobStorage
account and a private container (default `aks-skills`). Its top-level Bicep
accepts `location`, `storageAccountName`, `containerName`, `tags`,
`allowedSubnetIds`, `privateEndpointSubnetId`, `privateDnsZoneId`, and
`publicNetworkAccess`. This account serves shared artifacts. If you provision it,
this does not create the HOME StorageClass or mount a volume.

Prerequisites:

- Azure CLI with Bicep and authorization for the selected resource group.
- A resource group that already exists. The storage helper does not create one.
- AKS with Blob CSI enabled.
- A known node subnet or permission to find it.
- A reviewed `PARAM_FILE`. The default `main.bicepparam` is not in this
  repository. `location` and `storageAccountName` have no defaults.

**Offline syntax check**, with an already-installed Bicep executable:

```bash
bicep build infra/storage/main.bicep --stdout > /dev/null
```

This command does not examine Azure access or if nodes can mount NFS.

**Connected, mutating:** unless `AKS_SUBNET_ID` is set, the helper finds the
subnet from the node pool or the node resource group. It then updates the
subnet’s service endpoints, validates and creates the deployment, and queries
the outputs. With a [private endpoint](#private-endpoint), the helper skips the
subnet steps.

```bash
RESOURCE_GROUP=example-rg AKS_CLUSTER_NAME=example-cluster \
PARAM_FILE=/absolute/path/storage.bicepparam \
  bash infra/storage/deploy.sh
```

You must set `RESOURCE_GROUP` and `AKS_CLUSTER_NAME` even when you supply a
subnet. The helper uses the current Azure subscription. The tools that it must
have include GNU-compatible `grep -P`.

Review the subnet update carefully. It supplies `Microsoft.Storage` to
`--service-endpoints`. Do not expect that it keeps unrelated endpoint
configuration. It also overrides `allowedSubnetIds` with the **single** resolved
subnet and replaces any list in the parameter file. For multi-subnet access, a
separately reviewed deployment is necessary.

The subnet write occurs before Bicep validation. To examine the result, the
helper runs commands such as `az storage container show --auth-mode login` at
the end. This command must have Blob data-plane authorization and network
access from the workstation. This call can fail after the resources were
provisioned successfully. A successful call does not prove an NFS mount.

### Private endpoint

To reach the account only through a private endpoint, set
`PRIVATE_ENDPOINT_SUBNET_ID`. The helper then creates a private endpoint for
the blob service in that subnet. It does not update the service endpoints of a
subnet or add a subnet rule to the account.

`PRIVATE_DNS_ZONE_ID` names an existing private DNS zone for blob private
links, `privatelink.blob.core.windows.net`. The private endpoint registers its
address in that zone. The zone must be linked to the cluster's virtual network,
so that nodes resolve the account to the private address. The helper does not
create the zone or the link.

`PUBLIC_NETWORK_ACCESS` is `Enabled` by default. `Disabled` leaves only private
endpoints, so the helper requires `PRIVATE_ENDPOINT_SUBNET_ID` with it. With
`Disabled`, the helper reads the container through Azure Resource Manager
instead of the Blob data plane.

```bash
RESOURCE_GROUP=example-rg AKS_CLUSTER_NAME=example-cluster \
PARAM_FILE=/absolute/path/storage.bicepparam \
PRIVATE_ENDPOINT_SUBNET_ID="/subscriptions/<subscription-id>/resourceGroups/example-network-rg/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/example-nodes" \
PRIVATE_DNS_ZONE_ID="/subscriptions/<subscription-id>/resourceGroups/example-network-rg/providers/Microsoft.Network/privateDnsZones/privatelink.blob.core.windows.net" \
PUBLIC_NETWORK_ACCESS=Disabled \
  bash infra/storage/deploy.sh
```

### Shared skills storage for `make deploy`

`make deploy` sets `skills.shared: true` for all Agents. Each standard Agent,
which does not run in a Sandbox, then mounts a static Blob NFS volume. The volume uses the
top-level `sharedSkills` fields of the environment file.

> **Warning:** `make deploy` examines only that the StorageClass exists. It
> does not examine the storage account or the container. If they do not exist,
> the shared skills volume cannot mount, and the Agent Pods stay in
> `ContainerCreating`. Provision the storage before the first `make deploy`.

Set the four necessary `sharedSkills` fields. Copy three of them from the
`Deployment Summary` that the storage helper prints:

| Environment file field | Value |
| --- | --- |
| `sharedSkills.resourceGroup` | `Resource Group` in the summary |
| `sharedSkills.storageAccount` | `Storage Account` in the summary |
| `sharedSkills.containerName` | `Container` in the summary |
| `sharedSkills.storageClassName` | A Blob NFS StorageClass that exists in the cluster, for example `azureblob-nfs-premium` |

## NFS security properties

The [storage module]({{< repo "infra/storage/modules/storage-account.bicep" >}})
sets hierarchical namespace and NFS v3 when it creates the account. It disables
HTTPS-only enforcement for NFS traffic and does not allow anonymous blob access.
It uses `networkAcls.defaultAction: Deny` with explicitly allowed subnets.
The SKU is `Premium_LRS`, and `networkAcls.bypass` is `AzureServices`.
The module creates a private endpoint only when `privateEndpointSubnetId` is
set. A container with `publicAccess: None` is not the same as
private-endpoint-only networking.

HTTPS does not protect NFS traffic. Network access is central to this mount
path. Do not describe Azure AD/RBAC or account-key authentication as the NFS
data-plane authentication mechanism. Data ownership, mount permissions, network
isolation, and backups must get a separate review.

NFS 3.0 authorizes clients only by network. The Skill Marketplace writes the
Team releases into its own container. For that container, use a storage account
that only private endpoints reach, and disable its public network access:
set `PRIVATE_ENDPOINT_SUBNET_ID`, `PRIVATE_DNS_ZONE_ID`, and
`PUBLIC_NETWORK_ACCESS=Disabled`.

## Shared skills mounts

Configure `skills.resourceGroup`, `skills.storageAccount`, and
`skills.containerName`, or supply the equivalent `Team.skillStorage`. The Agent
reads the shared claim but does not change it.

For OpenClaw and Hermes, the agent chart mounts the shared claim read-only at
`skills.mountPath`, `/home/vibe/.aks/skills` by default. Files that you publish
appear in this mount without a restart. The platform Memory skill comes from
`/opt/cetirun-memory/skills`, and `$HOME/.agents/skills` stays a writable
personal directory.

A Custom Agent instead receives one read-only view at `$HOME/.agents`. Runtime
init combines the skills of the shared claim with the Memory skill in
`$HOME/.agents/skills`, and it rebuilds the view for each new Pod. A
non-default `skills.mountPath` adds an alias of that view. Refer to
[Skill sources of an Agent](../skill-marketplace/#skill-sources-of-an-agent).

### Publish shared material

CetiRun has no tool that writes to the shared skills container. Write the
material with a client that the storage account accepts:

- The account accepts network access only from its allowed subnet or through
  its private endpoint. Use a client in such a network, with a data role on
  the container.
- Put each skill in its own directory at the root of the container, for
  example `<skill>/SKILL.md`.
- For a Custom Agent, runtime init copies each entry at the root into the view
  of the Agent. Do not use the name `cetirun-memory`, or another name that the
  CetiRun context of the Agent already uses. Use only directories, regular
  files, and symbolic links. Otherwise, initialization fails with
  `SharedSkillConflictsWithCanonicalContext` or `UnsupportedSkillFile`.
- A Custom Agent sees a change only after its Pod starts again. Publish the
  material before a controlled replacement of its Pods.

NFS is the default. `skills.protocol` also accepts `fuse` and `fuse2` for
separately configured Blobfuse mounts. For those modes, `skills.secretName`
must refer to a staging Secret that already exists in
**each instance namespace**. The chart does not create that Secret. The team
storage fields do not select the protocol or Secret. Use reviewed Helm values
for those settings.

The [static PV template]({{< repo "app/cetirun-chart/templates/shared-skills-pv.yaml" >}})
creates a PV/PVC pair for each release, not a cross-namespace PVC. Each pair can
refer to the same container. The runtime mounts it read-only. Protocol, Secret
reference, and mount options are install-time settings for this chart. They are
not a live protocol-switch workflow. Before you retire storage that already
exists, plan replacement PV/PVC resources or a replacement instance.

Shared mounts and npm-installed skills are separate paths. The
[skill installer]({{< repo "app/cetirun-chart/configs/init-skills.sh" >}})
writes npm skills into `/home/vibe/.cetirun/skills/npm` and prunes entries that
are no longer requested. When any requested install fails, the installer fails
initialization.

By default, the installer uses the public npm registry,
`https://registry.npmjs.org/`. To use your approved registry, set
`skills.npmRegistry`. With `make deploy`, set it as `sharedSkills.npmRegistry`
for all Agents. `skills.clawHub` entries are not installed.

## Retention is not deletion protection

With the default StatefulSet claim templates, the value
`persistence.retainPolicy: Retain` keeps PVCs when the StatefulSet is deleted or
scaled down. The template adds that retention field only for a rendered
Kubernetes version of at least 1.25. This setting does not override a PV's
reclaim policy or protect a PVC from namespace deletion. The AKS `default`
StorageClass usually uses a `Delete` reclaim policy. As a result, the deletion
of a HOME PVC can also delete its Azure Disk. Before you delete an Agent,
examine the actual PV policy and arrange a backup, as
[Make and restore backups](../backup-restore/) shows.

The [Agent deletion handler]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
uninstalls Helm and deletes the entire instance namespace, which includes the
PVCs. The PV and its data then follow the actual reclaim policy and CSI
behavior. Suspension keeps the home of an operator-managed Agent: while the
Agent is suspended, the Operator sets `persistence.retainPolicy: Retain`, also
when the values select `Delete`. In a standalone agent chart release, a
`persistence.retainPolicy: Delete` value can delete claims on scale-down. Do
not use suspension as a backup.

## Recovery checks

Before you change the workload, examine the PVC binding, the selected
StorageClass and CSI driver, subnet access, and mount events. Keep sensitive
file listings out of public diagnostics. Test restore procedures. Neither pod
persistence nor a `Retain` policy is a substitute for backup.

When immutable fields differ, the agent chart's
[pre-upgrade/pre-rollback hook]({{< repo "app/cetirun-chart/templates/pre-upgrade-hook.yaml" >}})
can orphan-delete and recreate a StatefulSet. It does not migrate data or
automatically resize a PVC that already exists. After a successful chart update,
you must examine the mounted storage class, size, and contents.

Before you delete an Agent or its namespace, read
[Delete only with a recovery plan](../agent-lifecycle/#delete-only-with-a-recovery-plan).
