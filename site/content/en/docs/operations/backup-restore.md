---
title: "Make and restore backups"
description: "The data and Secrets of a CetiRun installation that need a backup, how to make the backup, and the limits of a restore."
weight: 52
---

CetiRun does not make backups. The platform keeps its state in persistent
volumes, in Secrets, and in Azure resources. Plan a backup of each item on
this page, and test the restore in a separate environment before you serve
users. A persistent volume or a `Retain` setting is not a backup.

> **Warning:** The deletion of an Agent usually also deletes its HOME disk.
> Without a backup, the files of the Agent are lost. Refer to
> [Retention is not deletion protection](../storage/#retention-is-not-deletion-protection).

## What to protect

| Data | Location | Effect of a loss |
| --- | --- | --- |
| Agent HOME | The HOME PVC of each Agent, in its namespace `cetirun-<agent>`. By default, a 100 GiB Azure Disk of the `default` StorageClass | The workspace files, the Memory notes in `$HOME/.cetirun/memory`, and the settings that the Agent keeps in HOME are lost |
| AI Gateway database | The PVC `data-cetirun-ai-gateway-postgresql-0` in `cetirun-system` | The model keys of the Agents, the budgets and spend, the models that the console added, and the console settings are lost |
| AI Gateway Secrets that you created | `cetirun-ai-gateway-auth`, `gateway-provider-keys`, and `gateway-valkey-auth` in `cetirun-system` | A new salt key makes the models that the console added unusable |
| Database password | `cetirun-ai-gateway-postgresql` in `cetirun-system`. The chart generates it | `make deploy` refuses to continue while the database volume exists without this Secret |
| OAuth2 Proxy cookie key | `oauth2-proxy-credentials` in `cetirun-istio`. The chart generates it | The upgrade of the Istio release fails until you restore the Secret |
| Password of the OAuth2 Proxy session store | `oauth2-proxy-sessions` in `cetirun-istio`. The chart generates it | `make deploy` refuses to continue while the session store or its PVC exists without this Secret |
| Certificates, keys, and other Secrets that you supply | The Secrets that [Certificates and credentials](../certificates/) lists with the source "You" | `make deploy` stops in its checks, and the components that use them fail |
| Key Vault key of the connection records | `identity.keyVaultKeyUrl` | The Broker cannot decrypt the connection records |
| Shared skills and the Skill Marketplace | Their Blob NFS containers | The Agents do not get the skills. Keep the source of the material, for example the Git repository of the Skill Marketplace |
| Environment file | Outside the repository | You must write the inputs of `make deploy` again |

The Valkey of the AI Gateway keeps no data on disk, and it needs no backup. The
session store of OAuth2 Proxy keeps only sign-in sessions on its PVC. A loss of
these sessions makes users sign in again.

Do not include the connection records of the Broker in a routine restore.
Owners connect their accounts again after a restore. Refer to
[Restore with fresh owner authorization](../agent-identity/#restore-with-fresh-owner-authorization).

## Make a backup of Agent HOME

Use a disk snapshot, for example a CSI `VolumeSnapshot` or
[Azure Backup for AKS](https://learn.microsoft.com/azure/backup/azure-kubernetes-service-backup-overview).
The cluster template enables the snapshot controller. It does not create a
snapshot class or a backup schedule. To see the snapshot classes of the
cluster, run this command:

```bash
kubectl get volumesnapshotclass
```

To list the PVCs of all Agent namespaces, run this command:

```bash
for ns in $(kubectl get namespaces -l app.kubernetes.io/managed-by=cetirun-operator -o name | cut -d/ -f2); do
  kubectl get pvc -n "$ns" -o name | sed "s|^|$ns/|"
done
```

The HOME PVC of a standard Agent has a name that starts with `data-`. The HOME
PVC of a policy-bound Agent has no labels. Its name is `home`, the default of
`platform.values.cetirun-operator.sandboxSources.homeClaimName`.

CetiRun has no procedure to restore a snapshot into an existing Agent. A
restore into an Agent needs a separately reviewed procedure. Before you delete
an Agent, make a backup of its HOME, and read the
[lifecycle guidance](../../guides/lifecycle/).

## Make a backup of the AI Gateway database

Make a dump of the database to a private file outside the repository. The
command reads the password inside the container, so the password is not on
your command line:

```bash
umask 077
kubectl exec -n cetirun-system cetirun-ai-gateway-postgresql-0 -c postgresql -- \
  sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" pg_dump -h 127.0.0.1 -U litellm -d litellm --format=custom' \
  > /absolute/private/ai-gateway.dump
```

The dump contains the spend log. By default, the spend log keeps the prompt and
the response of each call for seven days. When the AI Gateway value
`content.enabled` is `true`, the spend log keeps no prompts. The prompts and
responses are then in Azure Data Explorer, and this dump does not include them.
Protect the dump as you protect credentials.

Also keep a copy of the Secrets of the AI Gateway with the dump, because the
salt key must stay the same for the database. Refer to
[The salt key must never change]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#the-salt-key-must-never-change).

CetiRun has no tested restore procedure for this database. Test the restore of
a dump in a separate environment before you need it.

## Keep copies of the Secrets

Keep the Secrets that you created, and the certificates and keys that you
supplied, in protected storage outside the cluster, for example a key vault.
For the Secrets that the charts generate, export a copy. These commands remove
the fields that only the existing object has. They keep the labels and the Helm
annotations, because `make deploy` refuses an object that another owner made:

```bash
umask 077
kubectl get secret cetirun-ai-gateway-postgresql -n cetirun-system -o json |
  jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp)' \
  > /absolute/private/cetirun-ai-gateway-postgresql.json
kubectl get secret oauth2-proxy-credentials -n cetirun-istio -o json |
  jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp)' \
  > /absolute/private/oauth2-proxy-credentials.json
kubectl get secret oauth2-proxy-sessions -n cetirun-istio -o json |
  jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp)' \
  > /absolute/private/oauth2-proxy-sessions.json
```

These files contain Secret values. Move them to protected storage, and delete
the local copies. To restore a Secret, run `kubectl create -f` with its file
before you run `make deploy`. On a new cluster, `make deploy` has not created
the namespaces yet. First create the namespace of the Secret with
`kubectl create namespace`.

## Restore order

1. Restore the Secrets, as [Keep copies of the Secrets](#keep-copies-of-the-secrets)
   shows.
2. Run `make deploy`. The charts use the generated Secrets that you restored,
   and they do not generate new values.
3. Restore the data of the AI Gateway and of the Agents with the procedures
   that you tested.
4. Make sure that the Key Vault key versions that encrypted the records stay
   enabled.
5. Before users connect their accounts again, follow
   [Restore with fresh owner authorization](../agent-identity/#restore-with-fresh-owner-authorization).
6. Verify the installation, as
   [Verify the deployment](../../getting-started/platform/#8-verify-the-deployment)
   shows.
