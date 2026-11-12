---
title: "CetiAdmin"
description: "Examine the access of the CetiAdmin operations Agent, configure its inputs, or disable it."
weight: 32
---

CetiAdmin is a Hermes Agent that the operator installs to operate the
platform. It diagnoses the platform and manages Agents with `kubectl` and
`helm`. CetiAdmin uses its own Kubernetes ServiceAccount, its own AI Gateway
key, and its own monthly budget.

The operator chart and `make deploy` enable CetiAdmin by default
(`cetiadmin.enabled: true`). An installation stops with an error until you
supply the inputs of CetiAdmin or disable it.

A standard Agent is an Agent that does not select a policy-bound Team or a
Sandbox. A Sandbox Agent runs in a Sandbox of its policy-bound Team. Refer to
[Policy-bound Teams](../policy-teams/).

> **Warning:** CetiAdmin can read the Secrets of every standard Agent and run
> commands in its Pods. Its owner can tell CetiAdmin to use this access. Before
> you supply the inputs of CetiAdmin, examine its [access](#access) and select
> its owner. If the deployment does not need CetiAdmin,
> [disable it](#disable-cetiadmin).

## How CetiAdmin runs

- The operator creates an Agent and a Team with the name in
  `cetiadmin.instanceName`, `cetiadmin` by default. The Agent runs in the
  namespace `cetirun-<instanceName>`.
- The owner is the user in `cetiadmin.owner`. The owner opens CetiAdmin in the
  Portal in the same way as the other Agents of the owner.
- CetiAdmin uses the model in `cetiadmin.model.default`, `auto` by default. Its
  Gateway key starts with the budget in
  `cetiadmin.model.initialMonthlyBudgetUSD`. GitHub Copilot is disabled by
  default (`cetiadmin.model.githubCopilot: false`).
- The built-in `cetiadmin` skill gives the model the operations procedures.
- CetiAdmin manages all standard Agents of this operator. There is no Agent or
  Team allowlist.
- The optional monitor (`cetiadmin.monitor.enabled`) runs scheduled health
  checks. It keeps the results in CetiAdmin, or it sends them to one Slack
  channel.

The `cetiadmin` Team supplies only context. Users can select this Team for
their own Agents, but the Team gives these Agents no CetiAdmin access.

## Access

The operator gives the CetiAdmin ServiceAccount these Kubernetes permissions:

| Scope | Permissions |
| --- | --- |
| Namespace of each standard Agent | Read all Secrets (`get`, `list`, `watch`). Run `kubectl exec` in the Agent Pods. Delete Pods to restart them. Create and change Secrets and ConfigMaps. Change Services, StatefulSets, and Deployments. Write Events. Expand PVCs. Read Pod logs and the diagnostic resources. |
| Namespace of each Sandbox Agent | Read Pod logs and the diagnostic resources. Write Events. Expand PVCs. No Secret access and no `kubectl exec`. |
| Namespaces in `cetiadmin.platformReadNamespaces` | Read Pod logs and the diagnostic resources. |
| Cluster | Read the cluster-scoped diagnostic resources. Create, update, and patch Agent resources. |

The diagnostic resources are an explicit list of resource types, with `get`,
`list`, and `watch`. Examples are Pods, Events, ConfigMaps, Nodes, Namespaces,
RBAC objects, CustomResourceDefinitions, admission configuration, and network,
Istio, storage, and CetiRun resources. The list does not include Secrets.

Kubernetes RBAC and the CetiAdmin admission webhooks enforce these
permissions. The admission webhooks check writes and `kubectl exec`, for
example the target Pod and the fields that CetiAdmin can change. They do not
check reads. CetiAdmin gets no write access to itself, to the platform
components, to the credential stores, or to a retained migration source.

As a result, CetiAdmin can read these Secrets of each standard Agent:

- The AI Gateway key (`ai-gateway-virtual-key`)
- The native authentication Secret, for example `gateway-token`
- The GitHub Copilot token of the owner (`github-copilot-token`)

With `kubectl exec`, CetiAdmin can also read the files in the HOME of a
standard Agent, for example its sessions and its private memory. The data that
CetiAdmin reads can go into its model context. The AI Gateway sends this
context to the model provider. By default, the Gateway also keeps the request
in its spend logs. Refer to
[Data collection](../../security/#data-collection).

Sources: [permissions]({{< repo "app/cetirun-operator/internal/cetiadmin/permissions.go" >}}),
[operator values]({{< repo "app/cetirun-operator/chart/values.yaml" >}}), and
the [cetiadmin skill]({{< repo "app/cetirun-operator/internal/cetiadmin/assets/skills/cetiadmin/SKILL.md" >}}).

### Exec exception

The runtime chart of each Agent renders a policy that denies `kubectl exec`
in the Agent namespace. Refer to
[Kubernetes exec restriction](../../security/#kubernetes-exec-restriction). The
chart template has no fixed exception. When CetiAdmin is enabled, the operator
adds a match condition at runtime to the policy of each standard Agent. This
condition exempts the CetiAdmin ServiceAccount. The operator removes the
condition when CetiAdmin loses access to the Agent, for example when you
disable CetiAdmin.

## Confirmation tiers and rate limits

The `cetiadmin` skill sorts the operations of the model into tiers:

- **No confirmation:** health checks, bounded logs, restarts of eligible Pods
  of standard Agents, native authentication rotation, and recovery of failed
  Helm releases.
- **Administrator confirmation:** batch creation, planned version upgrades,
  rollback without a fault, changes to resource limits, and PVC expansion.
- **Out of scope:** changes to RBAC, policies, identity, platform
  configuration, and CetiAdmin itself, and the deletion of Agents or data. The
  skill leaves these changes to another authorized principal.

The skill also sets rate limits, for example 30 Kubernetes API writes and 10
`kubectl exec` calls each minute.

> **Warning:** These tiers and rate limits are instructions to the model. They
> are not an approval service, and the platform does not enforce them. Only
> Kubernetes RBAC and admission limit what CetiAdmin can do. Give the owner role
> only to a person who is permitted to have the [access](#access) of CetiAdmin.

## Configure CetiAdmin

With `make deploy`, configure CetiAdmin under
`platform.values.cetirun-operator.cetiadmin` in the environment file. When
CetiAdmin is enabled, its owner, its initial monthly budget, and its Kubernetes
API audience and addresses are necessary.
[CetiAdmin values](../../reference/helm/#cetiadmin-values) gives the format and
the default of each value. `make deploy` also examines these rules:

- `owner.alias` is equal to `ownerAlias`.
- `owner.tenantID` is equal to `identity.tenantId`.

For a complete example, the monitor, Slack delivery, and the migration from an
older administration Agent, refer to
[Configure CetiAdmin]({{< repo "app/cetirun-platform/README.md" >}}#configure-cetiadmin).

If you install the operator chart without `make deploy`, an enabled CetiAdmin
also needs the admission server, even when `sandboxAdmission.enabled` is
`false`. [CetiAdmin values](../../reference/helm/#cetiadmin-values) lists the
inputs of the admission server.

## Disable CetiAdmin

For a new installation, disable CetiAdmin in the environment file before the
first `make deploy`:

```yaml
platform:
  values:
    cetirun-operator:
      cetiadmin:
        enabled: false
```

The operator then does not create the CetiAdmin Agent.

For an installation that has CetiAdmin, do these steps:

1. In the environment file, set
   `platform.values.cetirun-operator.cetiadmin.enabled` to `false`. Keep the
   other CetiAdmin fields.
2. Examine the plan. This command does not connect to the cluster:

   ```bash
   make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml
   ```

   The plan shows `"enabled": false` in its `cetiadmin` section.
3. Run the deployment. **Connected, mutating:**

   ```bash
   make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
   ```

The operator removes the access of CetiAdmin and stops its Agent. It keeps the
Agent, its HOME, and its installation records, so that you can enable
CetiAdmin again. Before you remove the operator, disable CetiAdmin. Refer to
[Disable CetiAdmin before removing the Operator]({{< repo "app/cetirun-platform/README.md" >}}#disable-cetiadmin-before-removing-the-operator).

To install the operator chart directly without CetiAdmin, set
`cetiadmin.enabled: false`.
