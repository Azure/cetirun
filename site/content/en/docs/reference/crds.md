---
title: "Custom resources"
description: "Agent and Team in cetirun.microsoft.com/v1alpha1."
weight: 10
---

Both resources are **cluster-scoped**. Their API group is
`cetirun.microsoft.com/v1alpha1`; the kinds are `Agent` and
`Team`. Do not add `metadata.namespace`.

| Kind | Resource | CRD name | Short name |
| --- | --- | --- | --- |
| `Agent` | `agents` | `agents.cetirun.microsoft.com` | `cra` |
| `Team` | `teams` | `teams.cetirun.microsoft.com` | `crt` |

An Agent is a long-lived, user-owned environment, not an execution or template.
A Team supplies shared configuration and skills, not shared access to private
data. Pod replacement does not create a new Agent.

## Existing installations

Older installations may contain `OpenClawInstance` and `OpenClawTeam` objects
in the same API group and version. The operator and portal register only
`Agent` and `Team`. Installing their CRDs does not convert, adopt, or delete
legacy objects.

Do not delete legacy CRs or CRDs as an upgrade shortcut. Old finalizers can
uninstall workloads and delete namespaces; deleting a CRD removes its stored
objects. Review data retention and resource ownership before handing an
existing installation over to the new API.

The cluster scope, finalizer, namespace/release names, and PVC behavior are
retained. Harness configuration uses required `spec.agent`; the old
`spec.agentType` field is not supported. There is no automatic resource
conversion or field migration. Helm installs packaged CRDs on first
installation but does not upgrade or delete them.

## Agent

| Field under `spec` | Contract |
| --- | --- |
| `user.alias` | Required owner alias matching `^[a-z][a-z0-9-]{0,62}$` |
| `user.displayName` | Required display name |
| `user.email` | Optional email |
| `user.team` | Optional user-context label; does not select a team resource. Use `team` for that reference |
| `gatewayTokenSecretRef` | Required native Secret name for Hermes/OpenClaw; forbidden for Custom |
| `agent` | Required Harness configuration object |
| `agent.type` | `hermes` (default), `openclaw`, or `custom`; immutable |
| `agent.image` | Required for Custom; once set, cannot be cleared. Built-in image resolution requires runtime support |
| `agent.registrySecretRef.name` | Optional source credential reference; no namespace, key, or inline credentials |
| `agent.username` | Defaults to `vibe`; immutable, non-root image username |
| `agent.port` | Optional HTTP port, 1–65535; omitted for Custom means no platform HTTP entry |
| `team` | Name of a `Team` |
| `model` | Optional Copilot model ID, at most 256 characters |
| `skillOverrides.add` / `remove` | Set-like skill lists, at most 20 per list; effective IDs are `(team ∪ add) − remove` |
| `helm.version` | Agent chart pin; backfilled from the operator default on first reconciliation |
| `helm.values` | Map-shaped top-level entries; see admission notes below |
| `suspended` | Request zero replicas and removal of the instance VirtualService; false when omitted |
| `tailscale.authKeySecretRef` | Required non-empty Secret name when `tailscale` is present; instance-namespace Secret must contain non-empty `TAILSCALE_AUTH_KEY` |
| `msteams.enabled` | Opt in to Teams provisioning; default false |
| `msteams.appId` | Optional lowercase UUID client ID; the controller uses it as a fallback after tracked/name-based resolution and app creation |
| `msteams.tenantId` | Optional lowercase UUID tenant ID; otherwise use operator configuration |
| `msteams.webhookPort` | `int32`, schema default 3978; the CRD does not constrain its range, so use a valid non-conflicting listener port |

The namespace and release derive from `metadata.name`. For example, `alice`
uses `cetirun-alice` and `openclaw-alice`. An owner can have a differently named
instance; use the reported status instead of deriving names from the alias.
Use short DNS-label instance names. Portal creation reserves `oauth2`, `api`,
`internal`, `healthz`, `readyz`, `assets`, `portal`, `instances`, `admin`, and
`ui`. Direct CRD admission does not impose that same list; the controller
explicitly rejects `ui`. Avoid all reserved names rather than relying on
schema acceptance to establish safe routing.

See [first instance](../../getting-started/first-instance/) for minimal YAML and
[Teams](../../guides/msteams/) for the optional cloud integration.

The API accepts the Harness fields above, but the operator rejects Custom and
explicit image, port, registry, or non-default username settings with
`AgentRuntimeNotImplemented` before provisioning. See the
[Agent Harness contract](../agent-harness/) for the runtime boundary.

### Helm-value admission

The controller deep-merges maps and replaces arrays or scalar values. However,
the CRD sets `helm.values.additionalProperties.type: object`.
Consequently, each top-level entry must be map-shaped; an arbitrary standalone
Helm values file is not a valid `spec.helm.values` object. For example, this is
a valid **spec fragment**:

```yaml
helm:
  values:
    persistence:
      storageClass: approved-storage-class
      accessMode: ReadWriteMany
      size: 20Gi
    secrets:
      provider: openai
      model: your-authorized-model-id
```

The StorageClass and provider credential are deployment prerequisites, not
created by this fragment. In contrast, top-level `replicaCount: 1`,
`msteamsAllowedUsers: "..."`, or `extraEnv: [...]` under `helm.values` do not
match this schema. Deployment-wide `cetirun.helmValues` and direct Helm input
are separate configuration surfaces.

Even accepted values can violate controller assumptions: do not change
resource names, agent type, route ports, or the managed Secret reference using
Helm overrides. Use the corresponding CRD fields where provided. A schema
check does not prove that an override will reconcile successfully.

### Observed state

| Status field | Meaning |
| --- | --- |
| `phase` | `Pending`, `Provisioning`, `Ready`, `Failed`, `Deleting`, or `Suspended` |
| `conditions` | Individual readiness observations with reasons and messages |
| `accessUrl` | Configured native UI URL, `https://<domain>/ui/<instance>/`; not a live reachability test |
| `namespace`, `helmReleaseName` | Actual managed resource locations |
| `observedGeneration` | Generation recorded after Helm succeeds, before pod/ingress readiness is established |
| `observedOperatorHash` | Applied configuration fingerprint used by the reconciliation cache, including the gateway Secret content hash and referenced team's resource version |
| `lastReconcileTime` | Timestamp written by full Ready/suspension handling; can also be written while suspension ingress removal is failing |
| `msteams` | Teams state, message, resolved app and bot resource identifiers |

`AgentConfigReady=False` with reason `AgentRuntimeNotImplemented` reports a
setting that cannot yet be provisioned. The API does not expose a separate
`status.agent` structure.

Other condition types include `Ready`, `HelmDeployed`, `IngressReady`, `PodReady`,
`TailscaleReady`, and `TeamsReady`. Check them independently; overall readiness
can be true while ingress creation or Teams provisioning failed.
`TailscaleReady` reports that the key was copied, not that a node joined the
tailnet. `Suspended` skips pod-readiness checks; inspect actual replicas and
pod termination. The chart permits zero or one replica and restricts any
enabled HPA to `minReplicas=maxReplicas=1`.

The Teams state schema accepts `Provisioning`, `PendingAppRegistration`,
`Ready`, `Failed`, and `Deleting`. The controller's missing-OIDC-issuer branch
attempts to write `Pending`, which is not in that enum; its status update can
be rejected. Do not wait specifically for `Pending` to diagnose missing OIDC
configuration. Inspect conditions, events, and a narrowly scoped operator log
without disclosing credentials.

Teams status may contain `appId`, `appObjectId`, `botResourceId`,
`servicePrincipalId`, and `appNotFoundSince`. These are deployment identifiers,
not material to paste into a public bug report.

## Team

| Field under `spec` | Contract |
| --- | --- |
| `memberContext.role` | Role rendered into runtime-specific team context |
| `memberContext.description` | Context lines, at most 20 |
| `defaults.provider` | String without a CRD enum; chart supports `github_copilot`, `openai`, `anthropic`; instance Helm settings take precedence |
| `skills` | Default identifiers, at most 20 |
| `skillStorage.resourceGroup` | Required when `skillStorage` is present; existing storage resource group |
| `skillStorage.storageAccount` | Required when `skillStorage` is present; existing Blob account |
| `skillStorage.containerName` | Required when `skillStorage` is present; existing skills container |
| `skillStorage.storageClassName` | Optional override; chart default `azureblob-nfs-premium` |
| `skillStorage.storageSize` | Optional shared PVC request; chart default `100Gi` |
| `skillStorage.mountPath` | Optional in-container path; chart default `/home/vibe/.aks/skills` |

Instances reference this object through `spec.team`; this does not grant
membership or cross-instance access. There is no team-level model ID or
credential field. Copilot model choice is per-instance or deployment-wide.

The schema defines team status `conditions` and `observedGeneration`, but the
controller watches team changes only to enqueue member instances; it
does not maintain team status. Runtime context persistence also differs:
Hermes refreshes `TEAM.md` on initialization, while OpenClaw preserves an
initialized workspace's `USER.md`. See [configure a team](../../guides/teams/).

Skill entries in `skills` and `skillOverrides` follow this schema pattern:

```text
^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$
```

The `npm:` prefix selects the npm installer. Other accepted identifiers are
only passed to the reserved `skills.clawHub` list, not installed. Shared storage
uses chart-level protocol and mount options; those are not fields on
`skillStorage`. See [configuration and skills](../../concepts/configuration/).

## Schema and implementation

API validation uses these schemas:

- [Agent CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_agents.yaml" >}})
- [Team CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_teams.yaml" >}})
- [Go API types]({{< repo "app/cetirun-operator/api/v1alpha1" "tree" >}})
- [Reconciler and status writes]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
- [Helm value and route builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
- [Teams state handling]({{< repo "app/cetirun-operator/internal/controller/teams.go" >}})
- [Portal name validation]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}})

Use `agents.cetirun.microsoft.com` and `teams.cetirun.microsoft.com` in scripts
to avoid ambiguity with resources from other API groups.
