---
title: "Uninstall CetiRun"
description: "Remove a CetiRun installation in the correct order, and find the objects and cloud resources that stay."
weight: 54
---

CetiRun has no uninstall command. Remove an installation in the order of this
page. The Operator must run while you delete the Agents in step 3.

> **Warning:** These steps delete the Agents, their HOME volumes, and the
> platform. Before you start, make a backup of the data that you keep, as
> [Make and restore backups](../backup-restore/) shows. Get authorization for the
> target environment, and review each command before you run it.

## 1. Stop new use

1. Tell the users when the installation stops.
2. To prevent new sign-ins, remove the users from the allowed groups of the
   Portal sign-in application.

## 2. Disable CetiAdmin

Set `platform.values.cetirun-operator.cetiadmin.enabled: false` in the
environment file. Then run `make deploy` as an admission administrator: a user
in `sandboxAdmission.adminUsers`, or a member of a group in `adminGroups`. Only
these administrators can change the CetiAdmin configuration. The Operator
revokes the access of CetiAdmin and stops its Agent. The stopped Agent and its
HOME stay. Refer to
[Disable CetiAdmin](../cetiadmin/#disable-cetiadmin).

## 3. Delete the Agents

1. List the Agents:

   ```bash
   kubectl get agents.cetirun.microsoft.com
   ```

2. Delete the Agents that users created, in the Portal or with one command:

   ```bash
   kubectl delete agents.cetirun.microsoft.com <agent> <agent>
   ```

   The command waits until the Agents are gone. After a deletion in the
   Portal, wait with
   `kubectl wait --for=delete agents.cetirun.microsoft.com/<agent> --timeout=15m`.

Each Agent has the finalizer `cetirun.microsoft.com/finalizer`. The Operator
removes the finalizer after these steps:

1. It removes the Broker access and the Agent Identity of the Agent.
2. It revokes the AI Gateway key of the Agent.
3. It uninstalls the Helm release of the Agent and deletes its namespace.

If an Agent stays in deletion, read its events and the log of the Operator.
Do not remove the finalizer. Without the cleanup, these objects stay.

## 4. Uninstall the releases

Uninstall the platform release first, then the Broker, then the CetiRun Istio
release. These commands use the default names and namespaces. Use the Broker
release and namespace of your environment file:

```bash
helm uninstall cetirun-platform -n cetirun-system
helm uninstall <broker-release> -n <broker-namespace>
helm uninstall cetirun-istio -n cetirun-istio
```

If you used `publicTLS` with `certManager.mode: install`, the cert-manager
release stays. Uninstall it only if no other workload uses it. Refer to
[Existing certificates and retirement](../public-ingress/#existing-certificates-and-retirement).

## 5. Remove the mesh provider

A Helm uninstall does not remove the `oauth2-proxy` provider from the mesh
configuration. `make deploy` added this provider to the ConfigMap
`istio-shared-configmap-<revision>` in `aks-istio-system`. If no other
workload uses the provider, remove the `oauth2-proxy` entry from
`extensionProviders` in that ConfigMap. Do not remove other providers.

## 6. Remove the objects that stay

Helm keeps some objects on purpose, and `make deploy` and the Operator create
other objects outside Helm. Review this list, and remove the objects that you
do not keep:

Helm keeps these objects, because they have `helm.sh/resource-policy: keep`:

| Object | Location |
| --- | --- |
| The CRDs `agents`, `teams`, `sandboxes`, and `agentpolicies` of `cetirun.microsoft.com`. A deletion of a CRD also deletes all its objects. Refer to the warning below | Cluster |
| The Registry source namespace `cetirun-registry-credentials`, with the registry credentials of the Agents | Cluster |
| The ConfigMap `cetirun-cetiadmin-config` | `cetirun-system` |
| The Secret `cetirun-ai-gateway-postgresql` | `cetirun-system` |
| The PVC and the Secret `oauth2-proxy-sessions` | `cetirun-istio` |
| With `publicTLS`, the Issuers and the Certificates | `aks-istio-ingress` |

These objects also stay:

| Object | Location |
| --- | --- |
| The stopped CetiAdmin Agent, its namespace `cetirun-<instanceName>`, and its HOME. CetiAdmin keeps them for recovery | Cluster |
| The database PVC `data-cetirun-ai-gateway-postgresql-0` and the PVC of the ADX-Mon ingestor. Their StatefulSets created them | `cetirun-system` |
| The Secrets that you created, for example the AI Gateway, admission, MCP, and Broker Secrets | The namespaces of the installation |
| With `publicTLS`, the ACME account Secrets and `cetirun-tls`, which cert-manager created | `aks-istio-ingress` |
| The namespaces of the Istio release, the Broker, and the platform, which `make deploy` created outside Helm | Cluster |
| The write-ahead log of the ADX-Mon collector in `/var/lib/cetirun-adx-mon`. It can keep unsent logs, including Agent output | Each node that stays in the cluster |

> **Warning:** The stopped CetiAdmin Agent keeps the finalizer
> `cetirun.microsoft.com/finalizer`, and only the Operator removes it. After
> you uninstall the platform, a deletion of the CRD
> `agents.cetirun.microsoft.com` does not finish. Keep the CRDs, or plan the
> removal of the CetiAdmin Agent as a separately reviewed procedure before you
> uninstall the platform.

Some ConfigMaps in the Operator namespace can have finalizers that only the
Operator removes, for example the records of Sandbox execution. If you delete
the namespace without the Operator, these ConfigMaps can keep the namespace in
the `Terminating` state.

## 7. Remove the cloud resources

`make deploy` does not create cloud resources, so it does not remove them.
Remove the resources that you created for the installation, after you make
sure that nothing else uses them:

- The Entra applications: the Portal sign-in application, the Agent Identity
  Blueprint, and the Authorization application. Also their federated
  credentials and the provider registrations. Refer to
  [Verify, operate, and remove](../entra-agent-identity/#verify-operate-and-remove).
- The encryption identity and the Key Vault key. Remove them only after you
  remove the encrypted records and the backups that need them.
- The DNS records, or the DNS label of the ingress Public IP.
- The Blob storage of shared skills and of the Skill Marketplace.
- The Azure Data Explorer resources of the ADX-Mon collection, and the managed
  identity `<clusterName>-adx-ingestor`. With `adxContent`, also the managed
  identity `<clusterName>-gateway-content` and the database `CetiRunContent`,
  which keeps the prompts and responses of the AI Gateway.
- The images and charts in the registry, and the registry access of the
  cluster.
- The AKS cluster, if you created it only for CetiRun.

## 8. Verify

```bash
helm list --all-namespaces
kubectl get agents.cetirun.microsoft.com
kubectl get namespaces
```

The first command must not show the CetiRun releases. The other commands show
the objects that you decided to keep.
