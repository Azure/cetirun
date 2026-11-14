---
title: "Upgrade CetiRun"
description: "Upgrade a CetiRun installation and the agent chart of its Agents, and know what a return to an earlier version does not restore."
weight: 50
---

`make deploy` upgrades an installation in place. It upgrades the three Helm
releases to the version in the environment file. It keeps the Agents, their
HOME volumes, the AI Gateway database, and the OAuth cookie and database keys.
Existing Agents keep the version of their agent chart until you upgrade each
Agent. Each running Agent still restarts one time, because its Pod gets the
Memory Context image of the new version. The GitHub Copilot proxy of an Agent,
if it has one, also restarts with the new AI Gateway image.

> **Warning:** A return to an earlier version does not undo every change of an
> upgrade. Before you upgrade, read
> [Return to an earlier version](#return-to-an-earlier-version).

## Before you upgrade

1. Read the changes between the installed version and the new version.
2. Examine the [API compatibility](../../reference/crds/#existing-installations)
   of the CRDs.
3. Make a backup of the data and the Secrets that
   [Make and restore backups](../backup-restore/) lists.
4. Make sure that no release has an unfinished operation:

   ```bash
   helm list --all-namespaces --pending
   ```

   The list must be empty. Refer to
   [An unfinished release operation](../troubleshooting/#an-unfinished-release-operation).
5. If the installation uses raw `imagePullSecrets` overrides for Agents, remove
   them first. Refer to
   [Upgrading runtime registry configuration](../../getting-started/platform/#upgrading-runtime-registry-configuration).
6. Run the upgrade as an admission administrator: a user in
   `platform.values.cetirun-operator.sandboxAdmission.adminUsers`, or a member
   of a group in `adminGroups`. To see your user name and groups, run
   `kubectl auth whoami`. An upgrade changes the CetiAdmin configuration.
   While the installation has CetiAdmin, only admission administrators can
   change this configuration. Another identity, for example the service
   principal of a pipeline, cannot complete `make deploy`. Its run upgrades the
   Istio release and the Agent Identity Broker, then fails at the platform
   release.
7. Plan a maintenance window. An upgrade replaces the Pods of the platform, and
   it restarts each running Agent.

Use the same release version for all components: the Operator, the Sandbox
admission server, the Portal, the AI Gateway, the Broker, and Connections MCP.
`make deploy` installs all of them from one version.

## Upgrade the platform

1. Publish the new version, as
   [Publish images and charts](../artifacts/) shows.
2. In your clone, select the commit that built the new artifacts, for example
   with `git checkout`.
   `make deploy` installs the charts of the clone.
3. In the environment file, set `release.version` and `release.gitSHA` to the
   new version and commit.
4. Read the digests of the new Custom images, and add them to
   `platform.values.cetirun-operator.customRuntime.approvedImages`. Keep the
   digests that existing Custom Agents use until each of these Agents moves to
   a new digest. The Operator approves only an exact digest of the list, and
   it stops updating a Custom Agent whose digest is not in the list.
   `make deploy` accepts an old digest that is still in the registry.
5. Remove the fields that the new version does not support, and add the
   inputs that it requires. For some fields, the plan stops and names the
   field. Examples are the removed `customRuntime` fields and the
   `observability` block of the
   [ADX-Mon collection](../observability/#set-it-up-with-make-deploy), which
   `make deploy` requires.
6. Run the plan, then the deployment:

   ```bash
   make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml
   make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
   ```

7. Verify the installation, as
   [Verify the deployment](../../getting-started/platform/#8-verify-the-deployment)
   shows.

The CRDs are ordinary templates of the Operator chart, so Helm upgrades them
with the platform release. The default `compatible` admission mode accepts the
newer fields of a CRD. A `strict` admission mode needs an ordered upgrade.
Refer to the
[schema and binary upgrade contract]({{< repo "app/cetirun-operator/README.md" >}}#schema-and-binary-upgrades).

## Upgrade the Agents

An Operator upgrade does not change the agent chart version of an existing
Agent. A new Agent gets the agent chart of the installed release. To upgrade
an existing Agent, use the **Upgrade to Latest** action of the Portal, as
[Pin and update a chart](../agent-lifecycle/#pin-and-update-a-chart)
shows. Upgrade each Agent after the platform upgrade.

## Settings that make deploy keeps

Each run of `make deploy` replaces the stored Helm values of each release with
the values of the environment file. A value that you set with another Helm
command is lost at the next run. Two cases are different. With
`istio.tlsMode: retain`, the Istio release keeps its earlier values. With
`istio.mode: external`, `make deploy` does not install or upgrade the Istio
release.

`make deploy` also protects some settings of an installation. If the
environment file changes one of these settings, the run stops before it writes.
For most of them, the message starts with `Preserve existing`:

- The admission administrators: `adminUsers`, `adminGroups`, and
  `controllerUsername`
- The Team assignments of the admission configuration
- Pod admission and a `strict` admission validation mode
- The coordination, placement, and platform source settings of Sandbox
  execution, and native execution
- The Broker namespace and service account that the Operator uses
- The native Connections MCP settings and its peer identity
- The configuration coordination settings of the Portal
- The bundled PostgreSQL and Valkey of the AI Gateway
- `publicTLS` after it was on. Refer to
  [Public DNS and TLS](../public-ingress/#existing-certificates-and-retirement)

A change of these settings needs a separate approved procedure. `make deploy`
does not migrate stored Helm values.

A change of the StorageClass or the size of the AI Gateway database also
fails. Kubernetes does not let a StatefulSet change its claim template. The
preflight does not find this change. The run stops at the upgrade of the
platform release, after it upgraded the Istio release and the Broker.

## Upgrade order of some changes

Some changes need a specific order:

- **Portal ownership mTLS of a standalone installation:** first enable the
  strict internal policy of the Portal. Then deploy the Istio TLS cluster, and
  then update the Portal. Refer to
  [Authentication and ownership](../istio/#authentication-and-ownership).
- **CetiAdmin in the old `scope` format:** upgrade in two steps. Refer to
  [Upgrade an existing scope-format installation]({{< repo "app/cetirun-platform/README.md" >}}#upgrade-an-existing-scope-format-installation).
- **Peer registrations of policy-bound Teams:** a new release version also
  replaces the Pods of the AI Gateway. With the rollout-stable DNS and
  AI Gateway selectors in [Peer labels](../policy-teams/#peer-labels), the
  replacement Pods keep their network authorization without manual labels.
  Registrations by IP address or peer UID still need the current backend
  addresses and labels until you migrate them. Upgrade all readers and follow
  the coordinated configuration change before you use the new registration.

  A selector-bound AI Gateway entry must also select the protected label
  `sandbox.cetirun.microsoft.com/peer: ai-gateway`. Add the label to the entry
  after the upgrade, when the AI Gateway Pods have it. Until then, the Agents
  that use the entry stay held.
- **AI Gateway PodMonitor of managed Prometheus:** a platform that an earlier
  version installed can still hold this PodMonitor. Then `make deploy` stops
  before it changes the cluster. Create a stand-in definition of its API, run
  `make deploy`, and then delete the definition, as
  [Identity, images, and collection](../infrastructure/#identity-images-and-collection)
  shows.
- **OAuth2 Proxy PodDisruptionBudget with an external Istio release:** upgrade
  the external release first, or set
  `istio.values.oauth2Proxy.podDisruptionBudget.enabled: false` in the
  environment file. Otherwise the missing budget stops preflight before any
  write. Refer to
  [Authentication availability during node maintenance](../istio/#authentication-availability-during-node-maintenance).
- **ADX-Mon alert rules with a short interval:** the alerter of ADX-Mon
  v0.4.1 skips an `AlertRule` whose interval is shorter than 30 seconds. It
  logs `Skipping invalid AlertRule` once a minute and leaves the status of the
  rule as it was. Before you upgrade, give each such rule an `interval` of at
  least `30s`. After the upgrade, the API server refuses a shorter interval.
  Refer to [Alerts](../observability/#alerts).

## If an upgrade fails

`make deploy` has no automatic rollback. When a step fails, the run stops and
names the step. The releases that it upgraded before the failure keep the new
version. Find the cause, as
[When make deploy fails](../troubleshooting/#when-make-deploy-fails) shows.
Then correct the cause and run `make deploy` again with the new version.

## Return to an earlier version

CetiRun has no rollback command. A rollback to an older version needs a
reviewed plan. Before you make the plan, review these limits:

- **CRDs:** Helm does not protect the CRDs against an incompatible schema
  change or a downgrade. A Helm rollback cannot safely restore an older schema
  and binary.
- **AI Gateway database:** the AI Gateway runs its database migrations before
  it starts. A rollback does not undo them. The older version starts with the
  newer schema, and it logs SQL commands that drop the new columns. Do not run
  these commands. Refer to the
  [rollback section]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#rolling-back)
  of the AI Gateway chart README.
- **Application data:** a rollback does not reverse the data that the
  components wrote, for example Agent HOME files, connection records, and
  spend records.
- **Protected settings:** `make deploy` with an older version still refuses
  the changes in [Settings that make deploy keeps](#settings-that-make-deploy-keeps).
- **Certificates:** a Helm rollback does not restore certificate contents or
  external callbacks.

If you must return to an older version, restore the backup that you made
before the upgrade. Refer to [Make and restore backups](../backup-restore/).
