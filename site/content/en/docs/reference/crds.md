---
title: "Custom resources"
description: "Fields, defaults, validation rules, and status of the Agent, Team, Sandbox, and AgentPolicy resources in cetirun.microsoft.com/v1alpha1."
weight: 10
---

CetiRun adds four cluster-scoped resources to the Kubernetes API group
`cetirun.microsoft.com/v1alpha1`. Do not add `metadata.namespace` to them.

| Kind | Resource | CRD name | Short name |
| --- | --- | --- | --- |
| `Agent` | `agents` | `agents.cetirun.microsoft.com` | `cra` |
| `Team` | `teams` | `teams.cetirun.microsoft.com` | `crt` |
| `Sandbox` | `sandboxes` | `sandboxes.cetirun.microsoft.com` | `crs` |
| `AgentPolicy` | `agentpolicies` | `agentpolicies.cetirun.microsoft.com` | `crp` |

An Agent is a long-lived environment that one user owns. A replacement of its
Pod keeps the same Agent. A Team supplies shared configuration and skills to
its Agents. The private data of each Agent stays private. A Sandbox selects the
runtime, the resources, and the storage of the Agents that use it. An
AgentPolicy sets the controls of the Agents of a Team.

The field tables in this page come from the CRD schemas. The **Type**,
**Default**, and **Validation** columns show what the Kubernetes API server
checks when you write a resource. Each section also lists the checks that the
operator and the admission webhook do after the schema check.

This page uses these terms:

- A *Sandbox Agent* selects a policy-bound Team or a Sandbox. A *standard
  Agent* does not, and no AgentPolicy applies to it.
- The *common executor* is the operator flow that runs Sandbox Agents, Custom
  Agents in a Sandbox included, when Sandbox execution is enabled.
- A *held* Sandbox Agent is an Agent that the operator does not run. The
  operator keeps its StatefulSet at zero replicas and removes its route.
- A *single-start lifetime* is the lifetime that the operator recorded for a
  Sandbox Agent that started under the earlier version-1 native execution
  profile. Such an Agent keeps that profile and its limits. In scripts, use
the full resource names, for example `agents.cetirun.microsoft.com`, so that
resources of other API groups do not match.

## Existing installations

Older installations can contain `OpenClawInstance` and `OpenClawTeam` objects
in the same API group and version. The operator and the Portal do not register
these legacy kinds. The installation of the current CRDs
does not convert, adopt, or delete legacy objects. The policy-bound API is a
replacement contract. It is not a data migration.

Do not delete legacy CRs or CRDs as an upgrade shortcut. Old finalizers can
uninstall workloads and delete namespaces. If you delete a CRD, the deletion
removes its stored objects. Before you move an installation to the current API,
examine data retention and resource ownership.

The cluster scope, finalizer, namespace and release names, and PVC behavior
are the same as before. Harness configuration uses `spec.agent`, which is
necessary. The API has no `spec.agentType` field, and there is no automatic
resource conversion or field migration.

The operator chart manages the current CRDs as templates, and Helm upgrades
their schemas with the release. Their retention annotation prevents their
deletion during an uninstall. For earlier unmanaged CRDs, a separately reviewed
ownership handover is necessary. The chart does not force adoption, and it does
not do a schema or data migration. Retention does not protect against a schema
downgrade.

## Agent

This example is a Hermes Agent without a Team. The operator sets the image, the
port, and the chart version of the Agent when it first reconciles it:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Agent
metadata:
  name: alice
spec:
  user:
    alias: alice
    displayName: Alice Example
  agent:
    type: hermes
  gatewayTokenSecretRef: gateway-token
```

For a policy-bound Agent with a Team and a Sandbox, see
[Create your first Agent](../../getting-started/first-instance/). For the
`spec.agent` fields and the Custom image contract, see
[Agent Harness](../agent-harness/).

### Agent spec

<!-- generated: agent-spec -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `spec` | object |  | Rule: gatewayTokenSecretRef is required for built-in agents and forbidden for custom agents. Rule: restartRequestID cannot be removed once set. Rule: a suspended Agent cannot request a new restart. Rule: a migration request ID cannot select a different destination. Rule: a Helm maintenance request requires suspension. Rule: clearing Helm maintenance must retain suspension; resume is a separate update. | Rules that apply to the whole spec. The API server checks them on each create and update. |
| `agent` | object |  | Required. Rule: custom agents require image. Rule: agent type is immutable. Rule: agent username is immutable. Rule: a pinned image cannot be removed. Rule: webUI is supported only for custom agents. Rule: webUI requires port. | Necessary Harness configuration. See [Agent Harness](../agent-harness/#configuration). |
| `agent.image` | string |  | 1 to 2048 characters. | Necessary for Custom. Use the exact `<repository>@sha256:<digest>` reference from the Operator's `customRuntime.approvedImages`; a tag reference is never approved. After it is set, it cannot be cleared. Built-in defaults are pinned from the selected chart |
| `agent.port` | integer |  | Range 1 to 65535. | HTTP port. Necessary for a Custom Web UI, and optional in other cases. For Custom, an omitted port means that the Agent is headless. |
| `agent.registrySecretRef` | reference (name) |  | `name`: Required. Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | Optional reference to a Portal-managed source Secret. The reference has no namespace, key, or inline credentials. |
| `agent.type` | string | `"hermes"` | One of `openclaw`, `hermes`, `custom`. | The Harness type. It cannot be changed. |
| `agent.username` | string | `"vibe"` | Pattern `^[a-z_][a-z0-9_-]{0,31}$`. 1 to 32 characters. Rule: agent username must not be root. | Non-root image username, with HOME at `/home/<username>`. It cannot be changed. Hermes and OpenClaw need `vibe`. |
| `agent.webUI` | boolean | `false` |  | For Custom only. When `true`, `agent.port` serves a Web UI instead of the Chat API. See [Custom Web UI](../agent-harness/#custom-web-ui). |
| `agentIdentity` | object |  |  | Entra Agent Identity request of the Agent. |
| `agentIdentity.enabled` | boolean |  |  | Requests a dedicated Microsoft Entra Agent Identity for the Agent. The Portal sets it. See [Entra Agent Identity](../../operations/entra-agent-identity/). |
| `gatewayTokenSecretRef` | string |  | 1 to 253 characters. | Necessary for Hermes and OpenClaw, and not allowed for Custom. The name of the native authentication Secret in the Agent namespace `cetirun-<name>`. If the Secret does not exist, the operator creates it. If it exists, the operator adds the keys that are missing. It is not a model or registry credential. |
| `helm` | object |  |  | Helm settings of the Agent. |
| `helm.maintenance` | object |  | Rule: a Helm maintenance request ID cannot change its expected revision. | Administrator request for a Helm handoff of a suspended Agent. The controller and admission verify the handoff before a native Helm write. |
| `helm.maintenance.expectedRevision` | integer |  | Required. Minimum 1. | The Helm revision that the requester observed before the request. |
| `helm.maintenance.requestID` | string |  | Required. Pattern `^[A-Za-z0-9_-]+$`. 16 to 128 characters. | The ID that connects the request with `status.helmMaintenance`. |
| `helm.values` | map[string]object |  | Any fields. Each value: Any fields. | Map-shaped top-level Helm values. See [Helm-value admission](#helm-value-admission). |
| `helm.version` | string |  |  | Agent chart pin. The operator sets it from its default on the first reconciliation. A later change of the operator default does not change it. |
| `model` | string |  | At most 256 characters. | Optional model name. A policy-bound Agent must select a shared model that the policy allows, or a Copilot model if the policy allows Copilot. A Copilot model is selected by its Gateway name or as `copilot/<model ID>`. |
| `modelAccess` | object |  |  | The model sources that the Agent requests. |
| `modelAccess.githubCopilot` | boolean |  |  | Requests the GitHub Copilot models of the owner. A Sandbox Agent must set it explicitly. A standard Agent gets it by default when the deployment offers Copilot. |
| `modelAccess.monthlyBudgetUSD` | string |  | Pattern `^(0\|[1-9][0-9]*)(\.[0-9]{1,6})?$`. | Optional monthly budget of the Gateway key of the Agent in US dollars, as a decimal string. It cannot be more than the maximum of the policy. An administrator sets it on a suspended Agent. |
| `modelAccess.sharedModels` | boolean |  |  | Requests the shared models of the AI Gateway. A Sandbox Agent must set it explicitly. A standard Agent gets them by default. |
| `plannedMigration` | object |  |  | Optional administrator request with `requestID` and the destination `target.name` and `target.uid`. Only for common-executor OpenClaw or Hermes with one CSI/RWOP HOME. An accepted request cannot change until it completes. A request with the same ID cannot change its destination. |
| `plannedMigration.requestID` | string |  | Required. Pattern `^[A-Za-z0-9_-]+$`. 16 to 128 characters. | The ID of the request. If you use the current ID again, the operator only observes that request. |
| `plannedMigration.target` | reference (name, uid) |  | Required. `name`: Required. Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. `uid`: Required. 1 to 128 characters. | The destination of the move. |
| `restartRequestID` | string |  | Pattern `^[A-Za-z0-9_-]+$`. 16 to 128 characters. | Optional retained-restart ID. A change of the ID requests a new Pod through normal reconciliation. The same ID does not request another Pod. After it is set, it cannot be removed. It cannot change while the Agent is suspended. |
| `sandboxRef` | string |  | Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | Opts in to a Sandbox that the Team allows. Omitted for legacy execution. |
| `skillOverrides` | object |  |  | Per-Agent changes to the skills of the Team. The effective IDs are `(team ∪ add) − remove`. |
| `skillOverrides.add` | []string |  | At most 20 items. Unique items. Each item: Pattern `^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$`. | Skills to add to the skills of the Team. With the Skill Marketplace, the operator refuses an entry without the `npm:` prefix with `AgentConfigInvalid`. |
| `skillOverrides.remove` | []string |  | At most 20 items. Unique items. Each item: Pattern `^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$`. | Skills of the Team that this Agent does not use. With the Skill Marketplace, the operator refuses an entry without the `npm:` prefix with `AgentConfigInvalid`. |
| `suspended` | boolean |  |  | When `true`, requests zero replicas and the removal of the routes of the Agent. The data of the Agent stays. If omitted, the value is `false`. |
| `tailscale` | object |  |  | Tailscale configuration of the Agent. |
| `tailscale.authKeySecretRef` | string |  | Required. Pattern `^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`. At least 1 character. | The name of a Secret in the Agent namespace. The Secret must contain a non-empty `TAILSCALE_AUTH_KEY`. |
| `team` | string |  |  | Optional for legacy Agents. Necessary for policy binding. The Portal also creates Agents without a Team |
| `user` | object |  | Required. | The owner of the Agent. |
| `user.alias` | string |  | Required. Pattern `^[a-z][a-z0-9-]{0,62}$`. | Owner alias. |
| `user.displayName` | string |  | Required. | Display name of the owner. |
| `user.email` | string |  |  | Optional email address. It is not a verified owner identity. |
| `user.team` | string |  |  | Deprecated. Only for legacy display context. It is not a Team or policy assignment. |

<!-- /generated: agent-spec -->

The namespace and the Helm release get their names from `metadata.name`. For
example, the Agent `alice` uses the namespace `cetirun-alice` and the release
`openclaw-alice`. An owner can have an Agent with a different name. Use the
names in the status, not names that you derive from the alias.

Use short DNS-label Agent names. Portal creation reserves `oauth2`, `api`,
`internal`, `healthz`, `readyz`, `assets`, `portal`, `instances`, `admin`,
`connections`, and `ui`. Direct CRD admission does not enforce that list. The
controller rejects `ui`. Do not use the reserved names, and do not rely on
schema acceptance to make routing safe.

The operator resolves the Hermes and OpenClaw images and ports through the
Harness interface. Their profiles need the `vibe` username. Custom uses a
separate runtime path that supports non-default usernames. Without a binding
provider, a non-Sandbox Custom Agent reports `CustomRuntimeDisabled`.

The Operator chart enables the `customRuntime` Kubernetes provider by default.
Its approval list is empty by default, so it runs no Custom image until an
administrator approves the image digest in `customRuntime.approvedImages`.
Numeric `customRuntime.imageUsers` entries are necessary only for FQDN capture,
for example for an AgentPolicy with `blockedFQDNs`, not for every Custom Agent.
Portal Custom self-service is enabled by default. To restrict it, change
`features.allowedAgentTypes`.

The Portal requires a published image approval before creation or an image
change, including administrator updates. For non-Sandbox Custom Agents, the
Operator reports `CustomImageNotApproved` if a directly submitted Agent is not
approved or its approval was withdrawn; it retries after the configuration
changes. Other unavailable binding prerequisites report `CustomBindingsPending`.
For policy-governed Sandbox Custom Agents, a disabled provider or unapproved
image instead reports `PolicyResolved=False` with reason `PolicyDenied`.

Private images use `agent.registrySecretRef.name` to refer to a Portal-managed
source Secret. For image pulls, the operator copies the Secret to the runtime
namespace. It first validates the Agent UID ownership, the registry host, and
the workload permissions of the Secret. The Agent spec never stores
credentials. See [Private registries](../agent-harness/#private-registries).

### Helm-value admission

The controller deep-merges maps and replaces arrays or scalar values. The CRD
sets `helm.values.additionalProperties.type: object`, so each top-level entry
must be a map. For example, top-level `replicaCount: 1` or `extraEnv: [...]`
under `helm.values` does not match this schema. A standalone Helm values file
is not a valid `spec.helm.values` object.

This fragment changes the HOME storage of a built-in Agent without a Sandbox:

```yaml
helm:
  values:
    persistence:
      storageClass: approved-storage-class
      accessMode: ReadWriteOnce
      size: 100Gi
```

The StorageClass is a deployment prerequisite. This fragment does not create
it. To select a Gateway model, use `spec.model`.

The operator accepts different keys on each execution path. It rejects a key
before it changes the workload. The result depends on the Agent:

- A built-in Agent, or a Custom Agent that was never applied, goes to `Failed`.
  `AgentConfigReady` is `False` with reason `AgentConfigInvalid` and the
  rejection message.
- A Custom Agent that was already applied keeps its workload. It reports
  `AgentConfigInvalid` while it waits for a valid configuration.
- A Custom Sandbox Agent reports `PolicyResolved=False` with reason
  `PolicyDenied`, and the operator holds it.
- If the Agent uses `agent.registrySecretRef`, `RegistryCredentialsReady` can
  also be `False` with reason `RegistryRuntimeConfigRejected`.

| Execution path | Rejected keys in `spec.helm.values` |
| --- | --- |
| Built-in Agent without a Sandbox | Top-level `runtime`, `agentRequest`, `imagePullSecrets`, `customBindings`, `memoryContext`, `cetiadmin`, `aiGateway`, `connectionsMCP`, and `image`. Also `secrets.provider`, `secrets.authMode`, `secrets.apiBase`, `secrets.apiKey`, `secrets.model`, `service.port`, `service.targetPort`, `<type>.image`, `<type>.env.API_SERVER_PORT`, `<type>.config.model`, and `<type>.config.gateway.port`, where `<type>` is `openclaw` or `hermes`. Platform Pod annotations and, for Hermes, the Hermes listener, authentication, and runtime settings. |
| Custom Agent without a Sandbox | Every key except `resources`, `ttyd.enabled`, `desktop.enabled`, `desktop.logging`, `desktop.resources`, and `desktop.screenResolution`. |
| Sandbox Agent of the common executor | The keys of the built-in or Custom row above, and also `runtime`, `sandboxWorkload`, `runtimeClassName`, `replicaCount`, `serviceAccount`, `azureWorkloadIdentity`, `agentIdentity`, `connectionsMCP`, `persistence`, `resources`, `securityContext`, `podSecurityContext`, `networkPolicy`, `sidecarScope`, `autoscaling`, `ingress`, and `execRestriction`, because the Sandbox or the AgentPolicy controls them. For a built-in Agent, `secrets` accepts only an authorized `existingSecret`. A Custom Agent cannot set `desktop.enabled: true`. |

The operator applies the rules of built-in Agents also to the deployment-wide
`cetirun.helmValues` of the Operator chart. In these values, `extraEnv` must
use names that the platform does not reserve, and for Hermes, `extraEnvFrom`
must be empty. Only `cetirun.helmValues` can contain these lists, because each
top-level entry of `spec.helm.values` must be a map.

Accepted values can still break what the controller expects. Do not use Helm
overrides to change resource names, the Agent type, route ports, or the
managed Secret reference. Use the CRD fields for these settings. A schema check
does not prove that an override reconciles successfully.

### Agent status

<!-- generated: agent-status -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `accessUrl` | string |  |  | Configured native UI URL, `https://<domain>/ui/<instance>/`. It is not a live reachability test. |
| `agentIdentity` | object |  |  | Lifecycle of the Entra Agent Identity of the Agent. It never contains user tokens, authorization caches, or credentials. |
| `agentIdentity.clientId` | string |  |  | Public application ID of the Agent Identity. |
| `agentIdentity.creationAttempted` | boolean |  |  | Create checkpoint. It prevents a second identity when the outcome of a create request is unknown. |
| `agentIdentity.credentialPolicyRevision` | string |  |  | Revision of the Broker enforcement record that the operator read back for the Agent. |
| `agentIdentity.displayName` | string |  |  | Directory display name of the Agent Identity. It does not change when the name prefix of the deployment changes. |
| `agentIdentity.lastVerifiedTime` | timestamp |  |  | Time of the last directory verification. |
| `agentIdentity.objectId` | string |  |  | Directory object ID, for cleanup. It is not a credential. |
| `agentIdentity.observedGeneration` | integer |  |  | Agent generation of this status. |
| `agentIdentity.phase` | string |  | One of `Disabled`, `Provisioning`, `Ready`, `Degraded`, `Failed`, `Deleting`. | Lifecycle phase of the Agent Identity. |
| `agentIdentity.reason` | string |  |  | Machine-readable reason of the phase. It contains no sensitive data. |
| `aiGateway` | object |  |  | State of the AI Gateway key of the Agent. It never contains the key. |
| `aiGateway.accessGroups` | []string |  | Unique items. | Gateway model access groups of the key. |
| `aiGateway.blocked` | boolean |  |  | `true` when the Gateway rejects the key. A blocked key continues to report spend and reset times. |
| `aiGateway.budgetDuration` | string |  |  | Reset period of the budget, for example `1mo`. |
| `aiGateway.budgetResetAt` | timestamp |  |  | Next budget reset that the operator observed. |
| `aiGateway.budgetUSD` | string |  |  | Native Gateway budget of the key in US dollars, as the operator read it back. It is not a provider invoice. |
| `aiGateway.copilot` | object |  |  | GitHub Copilot proxy of the Agent and its Copilot models in the Gateway. |
| `aiGateway.copilot.accessGroup` | string |  |  | Gateway access group of the Copilot models of the Agent. Only the key of this Agent has it. |
| `aiGateway.copilot.models` | []string |  |  | Registered Copilot models, by the names that the Agent uses: the name of the shared model that is the same model, or the Copilot model ID. |
| `aiGateway.copilot.notAllowed` | []string |  |  | Copilot models that the AgentPolicy does not allow, because each is the same model as a shared model that the policy does not list. |
| `aiGateway.copilot.phase` | string |  | One of `SignInRequired`, `Starting`, `Ready`, `Degraded`. | `SignInRequired` until the owner signs in to GitHub Copilot through the Portal, `Starting` while the proxy reads the sign-in, `Ready` when the models of the account are registered, and `Degraded` when the registration fails. |
| `aiGateway.copilot.proxyKeyId` | string |  |  | ID of the master key of the proxy: the first characters of its SHA-256 hash. It does not show the key. |
| `aiGateway.copilot.reason` | string |  |  | Machine-readable reason of the phase. |
| `aiGateway.copilot.registered` | []string |  |  | Gateway model IDs that the operator registered for the proxy of the Agent. |
| `aiGateway.copilot.routes` | []object |  |  | Each registered Copilot model and its name in the Gateway. |
| `aiGateway.copilot.routes[].model` | string |  | Required. | Copilot model ID. |
| `aiGateway.copilot.routes[].name` | string |  | Required. | Name of the model in the Gateway. The Agent uses this name. |
| `aiGateway.copilot.routes[].reasoning` | boolean |  |  | `true` when the Agent can set a reasoning effort for the model. This requires that the price table of the Gateway says that the model reasons, and that the model is a Claude or GPT model. The Copilot proxy of the Agent passes a reasoning effort on only for these models. The Agent is configured to set a reasoning effort for `name` only when every model behind `name` reasons. |
| `aiGateway.copilot.routes[].shared` | boolean |  |  | `true` when `name` is the name of a shared model. The Gateway then serves the requests of the Agent from the shared deployments and from this Copilot model. |
| `aiGateway.copilot.routes[].vision` | boolean |  |  | `true` when the model accepts images, as the price table of the Gateway shows. |
| `aiGateway.copilot.skipped` | []string |  |  | Copilot models that the operator did not register, because a different model of the Gateway has the same name. |
| `aiGateway.copilot.unpriced` | []string |  |  | Copilot models that the operator did not register, because the Gateway has no price for them. |
| `aiGateway.defaultModel` | string |  |  | Default model of the Agent. |
| `aiGateway.keyAlias` | string |  |  | Gateway alias of the key. It comes from the Agent UID and does not change. |
| `aiGateway.keyId` | string |  |  | ID of the key: the first characters of its SHA-256 hash. It does not show the key. |
| `aiGateway.keySecretName` | string |  |  | Secret in the Agent namespace that holds the key. |
| `aiGateway.lastVerifiedTime` | timestamp |  |  | Time of the last successful check of the key in the Gateway. |
| `aiGateway.modelRouters` | []object |  | Unique by `name`. | Smart-routing models of the Agent: the routers of its Team and, with an AgentPolicy, the Gateway routers that the policy lists. |
| `aiGateway.modelRouters[].alias` | string |  |  | Name of a granted Team router when the key also reaches the router under that name. A Hermes Agent sends this name. Other Agents send the internal name. |
| `aiGateway.modelRouters[].contextWindow` | integer |  |  | Context window that the Agent uses for the router: the smallest known value of the models of the router. |
| `aiGateway.modelRouters[].granted` | boolean |  | Required. | `true` when the key of the Agent can use the router. |
| `aiGateway.modelRouters[].maxTokens` | integer |  |  | Maximum output tokens that the Agent uses for the router: the smallest known value of the models of the router. |
| `aiGateway.modelRouters[].model` | string |  |  | Gateway model name of a granted router: the internal name of a Team router, or the name of a Gateway router. |
| `aiGateway.modelRouters[].name` | string |  | Required. | Name of the router that the owner selects. |
| `aiGateway.modelRouters[].reason` | string |  |  | Why the router is not granted. |
| `aiGateway.modelRouters[].reasoning` | boolean |  |  | `true` when the Agent is configured to set a reasoning effort for the router, because every model that answers the requests of the router reasons. The Copilot routes of the Agent under the names of these models must also reason. |
| `aiGateway.modelRouters[].vision` | boolean |  |  | `true` when the Agent sends images to the router, because every model of the router accepts them. |
| `aiGateway.models` | []string |  |  | Model names that the Agent is configured with, as the Agent sees them in the Gateway. |
| `aiGateway.observedGeneration` | integer |  |  | Agent generation of this status. |
| `aiGateway.phase` | string |  | One of `Provisioning`, `Ready`, `Degraded`, `Deleting`. | Lifecycle phase of the key. |
| `aiGateway.policyRevision` | string |  |  | Revision of the Gateway policy projection that the operator read back. |
| `aiGateway.pricingRevision` | string |  |  | Revision of the catalog and pricing input of the Gateway read-back. It is not a price quote. |
| `aiGateway.reason` | string |  |  | Machine-readable reason of the phase. |
| `aiGateway.routed` | boolean |  |  | `true` when the model traffic of the Agent goes through the Gateway. |
| `aiGateway.spendUSD` | string |  |  | Spend that the Gateway observed in the current period. It is not the exact billing of the provider. |
| `aiGateway.team` | string |  |  | Gateway team of the key, from `spec.team`. |
| `conditions` | []Condition |  | Unique by `type`. | Individual readiness observations with reasons and messages. See [Agent conditions](#agent-conditions). |
| `helmMaintenance` | object |  |  | Controller observation of a Helm handoff. A stored phase alone does not give permission to write or resume. |
| `helmMaintenance.message` | string |  | At most 32768 characters. | Explanation of the observation. |
| `helmMaintenance.observedGeneration` | integer |  | Required. Minimum 0. | Agent generation of the handoff. `0` means that no generation is observed. |
| `helmMaintenance.phase` | string |  | Required. One of `Requested`, `Held`, `Reapplying`, `Released`, `Blocked`. | State of the handoff. |
| `helmMaintenance.reason` | string |  | At most 1024 characters. | Short machine-readable reason of the phase. |
| `helmMaintenance.requestID` | string |  | Required. Pattern `^[A-Za-z0-9_-]+$`. 16 to 128 characters. | ID of the request of this observation. |
| `helmMaintenance.revision` | integer |  | Required. Minimum 0. | Last observed Helm revision. `0` means that no revision is observed. |
| `helmReleaseName` | string |  |  | Helm release of the Agent. |
| `lastReconcileTime` | timestamp |  |  | Time that a full ready pass or suspension pass writes. The operator can also write it when the route removal of a suspension fails. |
| `namespace` | string |  |  | Namespace of the managed resources of the Agent. |
| `observedGeneration` | integer |  |  | Generation that the operator records after Helm succeeds, before the Pod and the routes are ready. |
| `observedOperatorHash` | string |  |  | Fingerprint of the applied configuration, for the reconciliation cache. It includes the content hash of the gateway Secret and the resource version of the Team. |
| `observedRestartRequestID` | string |  | Pattern `^[A-Za-z0-9_-]+$`. 16 to 128 characters. | Restart request that the current ready Pod has. An accepted request or an applied template alone does not set it. |
| `phase` | string |  | One of `Pending`, `Provisioning`, `Ready`, `Failed`, `Deleting`, `Suspended`. | Lifecycle phase. |
| `plannedMigration` | object |  |  | Controller-owned source and storage identity and acknowledgment checkpoint of a planned migration. The phases are `Protecting`, `Stopping`, `SourceTerminated`, `Detached`, `Starting`, and `Completed`. It is not a physical-fencing receipt, and it does not give permission to edit status. |
| `policy` | object |  |  | Resolved effective policy, and the revisions that each enforcement component must apply. |
| `policy.effectivePolicyRef` | reference (generation, name, uid) |  | `generation`: Required. `name`: Required. `uid`: Required. | The AgentPolicy that applies, by name, UID, and generation. |
| `policy.enforcement` | object |  |  | Desired revisions of the enforcement components. |
| `policy.enforcement.credential` | object |  |  | Credential enforcement. |
| `policy.enforcement.credential.desiredRevision` | string |  |  | Revision that the credential service must apply. |
| `policy.enforcement.gateway` | object |  |  | Gateway enforcement. |
| `policy.enforcement.gateway.desiredRevision` | string |  |  | Revision that the AI Gateway must apply. |
| `policy.enforcement.sandbox` | object |  |  | Sandbox enforcement. |
| `policy.enforcement.sandbox.desiredRevision` | string |  |  | Revision that the Sandbox workload must apply. |
| `policy.observedGeneration` | integer |  |  | Agent generation of the resolution. |
| `policy.phase` | string |  | One of `Pending`, `Applied`, `Denied`, `Unknown`. | Result of the policy resolution. |
| `registryCredentials` | object |  |  | Receipt of the last credential synchronization. It is not credential data or proof that a node can pull the image. |
| `registryCredentials.pullSecretUID` | string |  | Required. | UID of the local pull Secret. |
| `registryCredentials.sourceResourceVersion` | string |  | Required. | Resource version of the synchronized source Secret. |
| `registryCredentials.sourceUID` | string |  | Required. | UID of the synchronized source Secret. |
| `sandbox` | object |  |  | Last binding that the controller found correct. Omitted before enforcement succeeds. |
| `sandbox.agentPolicyUID` | string |  | Required. At least 1 character. | UID of the AgentPolicy of the binding. |
| `sandbox.appliedRevision` | string |  | Required. At least 1 character. | Applied revision of the binding. |
| `sandbox.sandboxUID` | string |  | Required. At least 1 character. | UID of the Sandbox of the binding. |
| `sandbox.teamUID` | string |  | Required. At least 1 character. | UID of the Team of the binding. |

<!-- /generated: agent-status -->

### Agent conditions

Examine each condition independently. A condition for a feature that the Agent
does not use can be absent or `False`.

| Condition | Meaning |
| --- | --- |
| `Ready` | The Agent is ready for use. `Ready` can be `True` while `IngressReady` is `False`. `Suspended` skips the Pod readiness checks, so examine the replicas and the Pod termination separately. |
| `AgentConfigReady` | The operator resolved the Agent configuration. Reason `AgentConfigResolved` means that the built-in configuration is prepared, not that the runtime is ready. `False` with `AgentConfigInvalid` reports a rejected profile, image, port, or Helm value, or a failed chart download for a new Agent or a spec change. For a failed download, the operator log records the error of the registry. `AgentRuntimeNotImplemented` reports a setting that cannot be provisioned, for example a non-default built-in username. `AgentChartUnavailable` reports a temporary chart download failure of an installed Agent. |
| `BuiltInPrerequisitesReady` | The last full reconciliation of a built-in Agent completed its prerequisites. Only a later successful reconciliation clears a failure. |
| `RegistryCredentialsReady` | The operator copied the registry credentials of `agent.registrySecretRef`. It does not prove that the registry accepted them or that the image was pulled. |
| `PolicyResolved` | The Team, the requests of the Agent, the Sandbox, and the effective AgentPolicy are resolved. `False` with `PolicyDenied` means that the policy does not allow a request, for example a skill. `DependencyUnavailable` means that a dependency cannot be read. |
| `PolicyApplied` | Every enforcement revision that the policy needs matches the evidence of its component. `EnforcementPending` means that the Sandbox, Gateway, or credential enforcement is not complete. |
| `SandboxReady` | The Sandbox workload of a policy-bound Agent is ready, and its Kubernetes configuration matches. It does not supply physical failover fencing. `SandboxHeld` means that the common executor holds the workload. If `PolicyResolved` is `False`, the message of `SandboxReady` and `Ready` ends with the `PolicyResolved` message. For other reasons, such as `PolicyConflict` or `UnsupportedControl`, the message can end with a fixed explanation from the operator code. This explanation contains no values from the policy or the registration. |
| `AIGatewayReady` | The AI Gateway virtual key of the Agent is ready. `NoModelEntitlement` means that the effective policy grants no model source. |
| `AgentIdentityReady` | The Entra Agent Identity of the Agent is ready. |
| `HelmDeployed` | The Helm release of the Agent is applied. |
| `IngressReady` | The routes of the Agent are configured. `IngressDisabled` means that the Agent has no route. A suspension removes the routes. |
| `PodReady` | The workload of the Agent has a ready replica. If ready replicas are lost or the StatefulSet cannot be read, a running Agent changes to `Provisioning` with `PodReady` and `Ready` set to `False`. |
| `TailscaleReady` | The operator copied the Tailscale key. It does not report that a node joined the tailnet. |
| `DesktopConfigured` | The desktop access of the Agent is configured. |
| `TerminalConfigured` | The terminal access of the Agent is configured. |
| `HelmMaintenanceReady` | Present only while `spec.helm.maintenance` is set. `True` when the controller holds the release for the requested revision. |
| `PlannedMigrationReady` | The state of the planned migration that `spec.plannedMigration` requests. |

While a built-in Agent runs, the operator continues to observe its StatefulSet.
Each full reconciliation downloads the chart again. If the chart of an
installed Agent cannot be downloaded, the deployed release stays as it is, and
the operator tries again every 30 seconds.

When the Pod recovers, the Agent returns to `Ready`, also while the chart registry is unavailable, if
`BuiltInPrerequisitesReady` is `True`. The operator does not override a
suspension, or a failure or a wait for another prerequisite. If an older status
does not have this condition, a successful full reconciliation is necessary
before readiness can recover. The access URL stays until the operator
reconciles the related route.

The chart allows zero or one replica, and it limits an enabled HPA to
`minReplicas=maxReplicas=1`.

## Team

This example is a policy-bound Team. A Team without `agentPolicyRef`,
`sandboxRefs`, and `defaultSandboxRef` has no AgentPolicy, and its Agents are
standard Agents:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: research
spec:
  agentPolicyRef: research
  sandboxRefs: [research]
  defaultSandboxRef: research
  memberContext:
    role: Research assistant
  skills: []
```

### Team spec

<!-- generated: team-spec -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `agentPolicyRef` | string |  | Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | The single policy of the Team. Necessary when the Team opts in to policy. |
| `defaultSandboxRef` | string |  | Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | Sandbox that new Agents get when they do not select one. It must be in `sandboxRefs`. Necessary for a policy-bound Team. It does not select a Sandbox again later. |
| `memberContext` | object |  |  | Team context that the runtime puts into the instructions of each Agent. |
| `memberContext.description` | []string |  | At most 20 items. | Context lines. |
| `memberContext.role` | string |  |  | Role of the Team members, for example `Engineer`. |
| `modelRouters` | []object |  | At most 10 items. Unique by `name`. | Smart-routing models of the Team, by `name`. See [Smart routing](../../guides/teams/#smart-routing). |
| `modelRouters[].classifier` | object | `{"type": "heuristic"}` | Rule: classifier.model is required for the llm and jev classifiers and not allowed for the heuristic classifier. | How the router selects the complexity tier of each request. |
| `modelRouters[].classifier.model` | string |  | Pattern `^[^\s]+$`. 1 to 256 characters. | Shared model that classifies requests. It is not a router of the Gateway. It is a chat model for `llm`, and a decision model (`model_info.mode: evaluation`) for `jev`. Necessary for `llm` and `jev`, and not allowed for `heuristic`. |
| `modelRouters[].classifier.type` | string | `"heuristic"` | One of `heuristic`, `llm`, `jev`. | `heuristic` scores each request without a model call. `llm` and `jev` ask a shared model. The license-limited classifier types of LiteLLM are not accepted. |
| `modelRouters[].name` | string |  | Required. Pattern `^[^\s/]+$`. 1 to 64 characters. Rule: a model router name must not start with the reserved prefix cetirun-. | Model name that Agents request. It cannot start with `cetirun-` in any letter case. |
| `modelRouters[].tiers` | object |  | Required. | The model of each complexity tier. |
| `modelRouters[].tiers.complex` | string |  | Required. Pattern `^[^\s]+$`. 1 to 256 characters. | Model for complex requests: a shared model that answers conversations and is not a router of the Gateway, or `copilot/<model>`. |
| `modelRouters[].tiers.medium` | string |  | Required. Pattern `^[^\s]+$`. 1 to 256 characters. | Model for ordinary requests, and for requests that the router cannot classify. |
| `modelRouters[].tiers.reasoning` | string |  | Pattern `^[^\s]+$`. At most 256 characters. | Optional model for requests that ask for step-by-step reasoning. If it is empty, the tier uses the `complex` model. |
| `modelRouters[].tiers.simple` | string |  | Required. Pattern `^[^\s]+$`. 1 to 256 characters. | Model for the simplest requests. |
| `sandboxRefs` | []string |  | At least 1 item. Unique items. Each item: Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | Set of allowed Sandboxes. Necessary for a policy-bound Team. |
| `skillStorage` | object |  |  | Shared Azure Blob NFS container of the Team skills. The operator mounts it read-only into each Agent. |
| `skillStorage.containerName` | string |  | Required. | A skills container that exists. |
| `skillStorage.mountPath` | string |  |  | Optional. For OpenClaw and Hermes, the path where the shared skills volume is mounted read-only. The chart default is `/home/vibe/.aks/skills`. Custom Agents read shared skills in `$HOME/.agents/skills`. A different path adds a read-only alias of that view. Sandbox Agents do not mount the container that `skillStorage` names. Their Team skills come from the Skill Marketplace or a published skills catalog. |
| `skillStorage.resourceGroup` | string |  | Required. | A storage resource group that exists. |
| `skillStorage.storageAccount` | string |  | Required. | A Blob account that exists. |
| `skillStorage.storageClassName` | string |  |  | Optional override. The chart default is `azureblob-nfs-premium`. |
| `skillStorage.storageSize` | string |  |  | Optional shared PVC request. The chart default is `100Gi`. |
| `skills` | []string |  | At most 100 items. Unique items. Each item: Pattern `^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$`. | Default skills of the Team. With the Skill Marketplace, each entry without the `npm:` prefix is the name of a marketplace skill. See [Skill Marketplace](../../operations/skill-marketplace/). |

<!-- /generated: team-spec -->

Each Team has one `agentPolicyRef`. An Agent does not select a policy class:
the API has no `normalAgentPolicyRef`, `adminAgentPolicyRef`, or Agent
`policyClass` field.

An Agent selects its Team with `spec.team`. The selection applies the runtime
defaults of the Team and, when configured, its policy. It does not grant access
to the data or the runtime of another Agent. There is no separate Team
membership record. Model and credential entitlements come from the policy, and
each Agent requests them.

A Team for administrative work is an independent Team with its own context and
skills. It does not select a more privileged policy for another Team.
Permission to manage Teams or AgentPolicies is a control-plane permission of
people. The platform enforces it separately from workload authorization.

If you set one of the policy or Sandbox fields, the Team opts in to the full
policy binding. A partial binding cannot fall back to legacy behavior. The Team
controller accepts a legacy Team without these references. It accepts a
policy-bound Team when its policy and Sandbox references form a valid
configuration.

Hermes refreshes `TEAM.md` at each initialization. OpenClaw keeps the `USER.md`
of an initialized workspace. See [Configure a Team](../../guides/teams/).

Skill entries in `skills` and `skillOverrides` follow this schema pattern:

```text
^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$
```

The `npm:` prefix selects the npm installer. Without the Skill Marketplace,
the operator passes the other identifiers only to the reserved `skills.clawHub`
list, and it does not install them. Shared storage uses chart-level protocol
and mount options. These options are not fields of `skillStorage`. See
[Configuration and skills](../../concepts/configuration/).

When the operator's `skillMarketplace.enabled` is on, every entry without the
`npm:` prefix is the name of a Skill Marketplace skill. Such a name has
lowercase letters and digits in words that single hyphens join, and at most 64
characters. These rules apply:

- The operator does not pass the name to `skills.clawHub`.
- For a Team with an AgentPolicy, the Skill Marketplace publishes the selected
  skills. Its common-executor Sandbox Agents copy them when they start.
  OpenClaw and Hermes read them in `$HOME/.aks/skills`, and Custom reads them
  in `$HOME/.agents/skills`.
- An Agent cannot add or remove such a name in `skillOverrides`. The operator
  sets the Agent to `Failed`, with `AgentConfigInvalid` and this message:
  `skillOverrides cannot add or remove Skill Marketplace skills; select them on the Team`.
  This rule also applies to Agents without a policy.
- A change of the selection of the Team restarts the running Sandbox Agents of
  the Team one time, as each Team change does.

See [Skill Marketplace](../../operations/skill-marketplace/).

### Team status

<!-- generated: team-status -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `compatibility` | object |  |  | Latest configuration-only assessment of the policy and the Sandboxes of the Team. See [Team compatibility](#team-compatibility). |
| `compatibility.checkedAt` | timestamp |  | Required. | Time of the assessment. |
| `compatibility.issues` | []object |  |  | Issues of the Team and its policy. |
| `compatibility.issues[].code` | string |  | Required. | Fixed issue code. See [Team compatibility](#team-compatibility). |
| `compatibility.issues[].field` | string |  |  | Path of the field that has the issue. |
| `compatibility.issues[].message` | string |  | Required. | Fixed message of the code. |
| `compatibility.issues[].policy` | reference (generation, name, uid) |  | `name`: Required. | The AgentPolicy of the issue. |
| `compatibility.issues[].sandbox` | reference (generation, name, uid) |  | `name`: Required. | The Sandbox of the issue. |
| `compatibility.issues[].team` | reference (generation, name, uid) |  | `name`: Required. | The Team of the issue. |
| `compatibility.message` | string |  |  | Explanation of the result. |
| `compatibility.policy` | reference (generation, name, uid) |  | Required. `name`: Required. | The AgentPolicy that the assessment read. |
| `compatibility.reason` | string |  |  | Machine-readable reason of the result. |
| `compatibility.sandboxes` | []object |  |  | Result for each selected Sandbox. |
| `compatibility.sandboxes[].issues` | []object |  |  | Issues of this Sandbox, with the fields of `compatibility.issues[]`. |
| `compatibility.sandboxes[].reason` | string |  |  | Machine-readable reason of the result. |
| `compatibility.sandboxes[].sandbox` | reference (generation, name, uid) |  | Required. `name`: Required. | The Sandbox that the assessment read. |
| `compatibility.sandboxes[].state` | string |  | Required. One of `Compatible`, `Incompatible`, `Unknown`. | Compatibility of the policy and this Sandbox. |
| `compatibility.state` | string |  | Required. One of `Compatible`, `Incompatible`, `Unknown`. | Overall compatibility of the Team. |
| `compatibility.team` | reference (generation, name, uid) |  | Required. `name`: Required. | The Team generation that the assessment read. |
| `compatibility.validUntil` | timestamp |  | Required. | Time after which the report is not current. |
| `conditions` | []Condition |  | Unique by `type`. | Condition `Accepted` and condition `ModelRoutersReady`. |
| `modelRouters` | []object |  | Unique by `name`. | Registration of each router of `spec.modelRouters`. |
| `modelRouters[].copilotModels` | []string |  |  | The `copilot/<model>` tiers, registered under the ID of the Copilot model. An Agent must have GitHub Copilot and that model. |
| `modelRouters[].gatewayModel` | string |  |  | Internal Gateway model name of the router. Granted Agents send their requests to it. It stays the same when the router configuration changes. |
| `modelRouters[].message` | string |  |  | Why the router is not ready. |
| `modelRouters[].name` | string |  | Required. | Router name from `spec.modelRouters`. |
| `modelRouters[].nonReasoningModels` | []string |  |  | For a ready router, the tier models, as the tiers name them, that do not reason. A shared model is listed when not every deployment of its name reports `supports_reasoning`. A `copilot/<model>` tier is listed when the price table of the Gateway has an entry for its model, and the entry does not report `supports_reasoning` or the model is not a Claude or GPT model. A tier that joins a shared model is also listed when that shared model does not reason. OpenClaw Agents offer no reasoning levels for the router. This is a hint only: the router is registered and granted as usual. |
| `modelRouters[].ready` | boolean |  | Required. | `true` when the router is registered in the Gateway with its current configuration. |
| `modelRouters[].sharedModels` | []string |  |  | Shared models that the tiers of the router send requests to, as registered. A GitHub Copilot tier that joins a shared model is included, and the classifier is not. An Agent with an AgentPolicy gets the router only when the policy allows each of these models. |
| `observedGeneration` | integer |  |  | Last generation that the operator fully reconciled. |

<!-- /generated: team-status -->

The Team controller sets the condition `Accepted`. Its reason is `Valid` for a
valid binding and `InvalidConfiguration` for an invalid binding. The reason is
`DependenciesUnavailable` when the controller cannot read the policy or a
Sandbox, and `Terminating` during deletion. The condition `ModelRoutersReady`
is `True` when every router of the Team is registered. `False` names each
router that is not registered, and `Unknown` means that the Gateway cannot be
reached.

### Team compatibility

The operator records a compatibility report for each policy-bound Team in
`status.compatibility`. It examines the saved configuration at most 15 seconds
apart, and after each Policy or Sandbox change. An unchanged report keeps its
timestamps until five seconds before it expires, when the operator renews it.
A changed result is published immediately. The report expires 30 seconds after
`checkedAt`, at `validUntil`. The report is for display. It does not change the
`Accepted` condition, and it does not authorize Agent execution.

`compatibility.state` and `compatibility.sandboxes[].state` are `Compatible`,
`Incompatible`, or `Unknown`. The Portal also shows `Pending` when no report
exists, and `Not configured` for a Team without a policy. These two values are
Portal labels, not values of the field. The Portal also shows `Unknown` when a
report expires or when a referenced generation changes.

Each entry of `compatibility.issues[]` and
`compatibility.sandboxes[].issues[]` has one of these codes and its fixed
message:

| Code | Message |
| --- | --- |
| `policy_missing` | Choose an Agent Policy. |
| `sandbox_missing` | Choose at least one Sandbox. |
| `default_sandbox_invalid` | Choose a default Sandbox from the selected Sandboxes. |
| `policy_not_found` | The selected Agent Policy was not found. |
| `policy_invalid` | The selected Agent Policy is invalid. |
| `multiple_sandboxes_not_allowed` | The selected Agent Policy allows one Sandbox per Team. |
| `sandbox_not_found` | The selected Sandbox was not found. |
| `sandbox_invalid` | The selected Sandbox is invalid. |
| `runtime_not_registered` | The Sandbox runtime is not registered by the platform. |
| `execution_profile_unsupported` | The Sandbox execution profile is not enabled by this platform. |
| `home_access_mode_unsupported` | The Sandbox HOME access mode is not supported by this platform. |
| `network_address_family_unsupported` | The Policy requests a network address family that the Sandbox does not support. |
| `network_protocol_unsupported` | The Policy requests a network protocol that the Sandbox does not support. |
| `read_only_root_filesystem_unsupported` | The Sandbox does not support the Policy read-only root filesystem requirement. |
| `ingress_unsupported` | The Sandbox does not support the Policy ingress configuration. |
| `network_control_unsupported` | The Sandbox does not support one or more requested Policy network controls. |
| `kubernetes_access_unsupported` | The Sandbox does not support the Policy Kubernetes access requirement. |
| `entra_id_unsupported` | The Sandbox does not support the Policy Entra ID requirement. |
| `sandbox_incompatible` | The selected Sandbox does not support one or more requested Policy controls. |

For an upgrade, update the CRD schema, admission, and all other strict Team
readers. Then run the operator that writes the compatibility status. The Teams
that exist get a report when the operator reconciles them. Older objects
without this optional field stay readable. Do not downgrade a strict reader
until it can read the status field.

## Sandbox

A Sandbox selects a prepared runtime: `kata`, `gvisor`, or `container`. It
also sets the resource budgets of the Agent and its helper containers, the
private HOME storage, and the scratch space. This example uses Kata:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Sandbox
metadata:
  name: research
spec:
  type: kata
  resources:
    agent:
      requests: {cpu: "1", memory: 1Gi, ephemeral-storage: 1Gi}
      limits: {cpu: "2", memory: 2Gi, ephemeral-storage: 2Gi}
    helpers:
      requests: {cpu: 100m, memory: 128Mi, ephemeral-storage: 128Mi}
      limits: {cpu: 500m, memory: 2Gi, ephemeral-storage: 512Mi}
  storage:
    home:
      storageClassName: native-home
      size: 10Gi
      accessMode: ReadWriteOncePod
    scratch:
      sizeLimit: 1Gi
```

### Sandbox spec

<!-- generated: sandbox-spec -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `resources` | object |  | Required. | Resource budgets of the Agent and its helper containers. |
| `resources.agent` | object |  | Required. Rule: resource requests must not exceed their limits. | Requests and limits of the main Agent container. |
| `resources.agent.limits` | map[string]quantity |  | Required. Exactly 3 keys. Rule: exactly cpu, memory and ephemeral-storage are required. Rule: resource quantities must be positive. | Limits of `cpu`, `memory`, and `ephemeral-storage`. |
| `resources.agent.requests` | map[string]quantity |  | Required. Exactly 3 keys. Rule: exactly cpu, memory and ephemeral-storage are required. Rule: resource quantities must be positive. | Requests of `cpu`, `memory`, and `ephemeral-storage`. |
| `resources.helpers` | object |  | Required. Rule: resource requests must not exceed their limits. | Requests and limits of each init container and Agent sidecar, for example doctor and the credential retirement steps. |
| `resources.helpers.limits` | map[string]quantity |  | Required. Exactly 3 keys. Rule: exactly cpu, memory and ephemeral-storage are required. Rule: resource quantities must be positive. | Limits of `cpu`, `memory`, and `ephemeral-storage`. The Portal recommends a `2Gi` memory limit. A `512Mi` limit can cause an OOMKill of the OpenClaw initialization. |
| `resources.helpers.requests` | map[string]quantity |  | Required. Exactly 3 keys. Rule: exactly cpu, memory and ephemeral-storage are required. Rule: resource quantities must be positive. | Requests of `cpu`, `memory`, and `ephemeral-storage`. |
| `storage` | object |  | Required. | Private storage of the Agent. |
| `storage.home` | object |  | Required. | The private HOME volume of each Agent. |
| `storage.home.accessMode` | string |  | Required. One of `ReadWriteOncePod`, `ReadWriteOnce`. | Access mode of the HOME volume. The access mode alone does not make the volume private or exclude other writers. |
| `storage.home.size` | quantity |  | Required. Rule: home size must be positive. | Size of the HOME volume. |
| `storage.home.storageClassName` | string |  | Required. Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | StorageClass of the HOME volume. The platform must approve it. |
| `storage.scratch` | object |  | Required. | Writable scratch space of the Agent. |
| `storage.scratch.sizeLimit` | quantity |  | Required. Rule: scratch size must be positive. | Size limit of the scratch volume. It controls accounting and eviction. It is not a hard disk quota. |
| `type` | string | `"kata"` | One of `kata`, `gvisor`, `container`. | Prepared runtime: `kata` (VM isolation), `gvisor`, or `container` (ordinary containers without VM isolation). Sandbox execution does not support `gvisor`. |

<!-- /generated: sandbox-spec -->

`container` is ordinary container execution, for example runc, without VM
isolation. The Agent shares the kernel of the node. It needs a trusted
registration with an explicitly pinned RuntimeClass UID and handler, and the
enabled common executor. When `type` is omitted or null, the value is `kata`.

You can save a `gvisor` Sandbox, but Sandbox execution does not support it.
Such a Sandbox does not become ready, and its `Ready` condition reports
`UnsupportedControl`.

A `container` Sandbox uses the same AgentPolicy, Gateway, Cilium, and retained
HOME lifecycle as Kata, with per-container resource limits and no implicit VM
overhead. It supports the same separately approved
[managed ingress mTLS](../../guides/agent-ingress-mtls/) as Kata. A capability
flag alone does not authorize the endpoint.

Observed-SNI blocking and Kubernetes grants need the same explicit common-adapter
registrations and source approvals as Kata. The RuntimeClass alone does not
enable them. A save of a configuration does not discover or install a handler.
To change the runtime of an Agent, select a new Sandbox or create a new Agent.
The platform does not convert a running Agent.

### Sandbox status

<!-- generated: sandbox-status -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `conditions` | []Condition |  | Unique by `type`. | Condition `Ready` for the platform prerequisites of the Sandbox. |
| `observedGeneration` | integer |  | Minimum 0. | Last generation that the controller assessed. |

<!-- /generated: sandbox-status -->

The Sandbox controller sets the condition `Ready` for the platform
prerequisites of the Sandbox, not for an Agent. Its reason is
`PrerequisitesReady` when the registered runtime and storage match. Other
reasons are `InvalidConfiguration`, `UnsupportedControl`, `RuntimeUnavailable`,
`UnapprovedStorage`, `ReferenceMissing`, `ReferenceChanged`, `UnapprovedGrant`,
`PolicyConflict`, `PrerequisitesUnavailable`, and `Terminating`. If the
operator knows a fixed reason, the message names it, for example an ingress
mTLS registration without a digest-pinned image. The message contains only
text from the operator code, without values from the registration.

## AgentPolicy

An AgentPolicy limits the Harnesses, skills, models, network access, and
identities of the Agents of a Team. This example allows OpenClaw and one shared
model:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: AgentPolicy
metadata:
  name: research
spec:
  harnesses:
    allowed: [openclaw]
  skills:
    allowed: []
  modelAccess:
    sharedModels:
      allowed: [example-model]
    gatewayBudget:
      defaultMonthlyUSD: "10"
      maxMonthlyUSD: "10"
  security:
    readOnlyRootFilesystem: true
  network:
    ingress:
      mode: authenticated
```

### AgentPolicy spec

<!-- generated: agentpolicy-spec -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `agentIdentity` | object |  |  | Entitlement to the Entra Agent Identity that the Broker and Connections use. |
| `agentIdentity.allowed` | boolean |  |  | Allows Agent Identity for the Agents of the policy. |
| `agentIdentity.credentialServices` | []string |  | Unique items. Each item: One of `azure-resource-manager`. | Credential services of the identity. An allowed identity needs `azure-resource-manager`. |
| `agentIdentity.required` | boolean |  |  | Selects Agent Identity for each new Agent, and the owner cannot clear it. Needs `allowed: true`. |
| `entraID` | object | `{}` |  | Approved Entra workload identity in the main container. It is independent of `agentIdentity`. |
| `entraID.enabled` | boolean | `false` |  | Projects an exchange token and the Azure SDK environment into the main container. It needs platform registration, a pinned ServiceAccount, federation, and resource permissions. |
| `harnesses` | object |  | Required. | Harness limits of the policy. |
| `harnesses.allowed` | []string |  | Required. At least 1 item. Unique items. Each item: One of `openclaw`, `hermes`, `custom`. | Harness types that the Agents of the policy can use. Custom also needs the common executor and an approved Gateway-qualified image. |
| `kubernetesAccess` | object |  | Rule: at least one explicit RBAC grant is required. | Kubernetes API access of the Agents. Its presence grants nothing: separately approved platform grants are necessary. Platform administrators manage it outside the Portal. |
| `kubernetesAccess.clusterRoleBindings` | []object |  |  | ClusterRoles that the operator binds cluster-wide to the ServiceAccount of each Agent. |
| `kubernetesAccess.clusterRoleBindings[].clusterRoleName` | string |  | Required. Pattern `^[^/%]+$`. At least 1 character. Rule: a ClusterRole name cannot be '.' or '..'. | Name of a ClusterRole that exists. |
| `kubernetesAccess.roleBindings` | []object |  |  | ClusterRoles that the operator binds in one namespace to the ServiceAccount of each Agent. |
| `kubernetesAccess.roleBindings[].clusterRoleName` | string |  | Required. Pattern `^[^/%]+$`. At least 1 character. Rule: a ClusterRole name cannot be '.' or '..'. | Name of a ClusterRole that exists. |
| `kubernetesAccess.roleBindings[].namespace` | string |  | Required. Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`. 1 to 63 characters. | Namespace of the binding. |
| `kubernetesAccess.serviceAccountName` | string |  | Required. Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | ServiceAccount of each Agent. |
| `modelAccess` | object |  |  | Gateway entitlements and the budget of the key of each Agent. See [AgentPolicy model access](#agentpolicy-model-access). |
| `modelAccess.copilot` | object |  |  | GitHub Copilot entitlement. |
| `modelAccess.copilot.allowed` | boolean |  |  | Lets the Agents use the GitHub Copilot of their owners. The Portal shows it as **Allow GitHub Copilot**. |
| `modelAccess.gatewayBudget` | object |  |  | Monthly budget of the key of each Agent. Necessary when the policy allows a shared model or Copilot, and not allowed otherwise. |
| `modelAccess.gatewayBudget.defaultMonthlyUSD` | string |  | Required. Pattern `^(0\|[1-9][0-9]*)(\.[0-9]{1,6})?$`. | Budget of a new key, in US dollars. It must be above zero and not more than `maxMonthlyUSD`. |
| `modelAccess.gatewayBudget.maxMonthlyUSD` | string |  | Required. Pattern `^(0\|[1-9][0-9]*)(\.[0-9]{1,6})?$`. | Maximum budget that an administrator can set on an Agent, in US dollars. It must be above zero. |
| `modelAccess.rateLimits` | object |  |  | Request limits on the key of each Agent. An omitted field is unlimited. See [AgentPolicy rate limits](#agentpolicy-rate-limits). |
| `modelAccess.rateLimits.maxParallelRequests` | integer |  | Range 1 to 2147483647. | Requests of the key in flight at the same time. |
| `modelAccess.rateLimits.perModel` | map[string]object |  | At most 64 keys. Rule: perModel keys must be Gateway model names of 1 to 256 characters without whitespace. Each value: Rule: a per-model limit must set rpm, tpm, or both. | Limits for individual models, by exact Gateway model name. For a router of the Team, the router name is also accepted. |
| `modelAccess.rateLimits.perModel.<name>.rpm` | integer |  | Minimum 1. | Requests per minute of the key for this model. |
| `modelAccess.rateLimits.perModel.<name>.tpm` | integer |  | Minimum 4096. | Tokens per minute of the key for this model. |
| `modelAccess.rateLimits.rpm` | integer |  | Minimum 1. | Requests per minute of the key across all models. |
| `modelAccess.rateLimits.tpm` | integer |  | Minimum 4096. | Tokens per minute of the key across all models, from the tokens of finished requests. |
| `modelAccess.sharedModels` | object |  |  | Shared model entitlement. |
| `modelAccess.sharedModels.allowed` | []string |  | Unique items. Each item: Pattern `^[^\t\r\n ]+$`. 1 to 256 characters. | Exact Gateway model names that the Agents can use, routers included. For a router, also list the models of its tiers, its default model, and its context compaction model, if it has one. |
| `network` | object | `{}` |  | Network controls of the Agents. |
| `network.allowedInternalCIDRs` | []object | `[]` |  | Internal destinations that the Agents can reach, each with its ports. It needs explicit platform registration and approvals. SCTP grants are limited to direct endpoints. |
| `network.allowedInternalCIDRs[].cidr` | string |  | Required. At least 1 character. | IPv4 or IPv6 destination CIDR. |
| `network.allowedInternalCIDRs[].ports` | []object |  | Required. At least 1 item. | Allowed ports of the destination. |
| `network.allowedInternalCIDRs[].ports[].port` | integer |  | Required. Range 1 to 65535. | Port number. |
| `network.allowedInternalCIDRs[].ports[].protocol` | string | `"TCP"` | One of `TCP`, `UDP`, `SCTP`. | Protocol of the port. |
| `network.blockedCIDRs` | []string | `[]` |  | Destination CIDRs that the Agents cannot reach. |
| `network.blockedFQDNs` | []string | `[]` | Each item: Pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`. 1 to 253 characters. | Exact lowercase hostnames that the Agents cannot reach. The platform checks the visible SNI. It needs registered capture images, and for Custom also `customRuntime.imageUsers`. It is not full hostname enforcement. |
| `network.blockedPorts` | []object | `[]` |  | Destination ports that the Agents cannot reach. |
| `network.blockedPorts[].port` | integer |  | Required. Range 1 to 65535. | Port number. |
| `network.blockedPorts[].protocol` | string | `"TCP"` | One of `TCP`, `UDP`, `SCTP`. | Protocol of the port. |
| `network.ingress` | object | `{}` | Rule: disabled ingress cannot require mTLS. | Ingress controls of the Agents. |
| `network.ingress.mode` | string | `"authenticated"` | One of `authenticated`, `disabled`. | `authenticated` publishes the routes of the Agent through platform authentication. `disabled` allows no ingress to the Agent. |
| `network.ingress.requireMTLS` | boolean | `false` |  | Requires managed ingress mTLS. It needs an [administrator-registered managed endpoint](../../guides/agent-ingress-mtls/). |
| `sandboxSelection` | object | `{}` |  | Sandbox selection rules of the Teams of the policy. |
| `sandboxSelection.allowMultiple` | boolean | `false` |  | Lets a Team of the policy allow more than one Sandbox. |
| `security` | object | `{}` |  | Security controls of the Agent containers. |
| `security.readOnlyRootFilesystem` | boolean | `true` |  | Makes the root filesystem of the Agent containers read-only. |
| `skills` | object |  |  | Skill allowlist. |
| `skills.allowed` | []string |  | Unique items. | Exact entries that the `skills` of the Team and the `skillOverrides.add` of an Agent can contain, `npm:` packages included. |

<!-- /generated: agentpolicy-spec -->

The schema checks the shape of each field. The Sandbox admission webhook
checks these rules when `sandboxAdmission.enforce` is `true`, and the
AgentPolicy controller checks them for each saved policy. If a saved policy
breaks a rule, the controller sets `Accepted` to `False` with reason
`InvalidConfiguration`.

- `harnesses.allowed` contains at least one of `openclaw`, `hermes`, and
  `custom`.
- `modelAccess.gatewayBudget` is necessary when the policy allows a shared
  model or Copilot. It is not allowed otherwise.
- `defaultMonthlyUSD` and `maxMonthlyUSD` are above zero, and the default is not
  more than the maximum. The schema pattern also accepts `"0"`.
- Entries of `skills.allowed` and `modelAccess.sharedModels.allowed` are not
  blank.
- Each CIDR is an IPv4 or IPv6 CIDR. IPv4-mapped IPv6 addresses are not
  accepted.
- Each entry of `blockedFQDNs` is an exact lowercase hostname without a
  trailing dot, a wildcard, or a URL. IP addresses are not hostnames.
- Each entry of `allowedInternalCIDRs` has at least one port.
- `agentIdentity.required` needs `allowed: true`. An allowed identity needs
  `azure-resource-manager` in `credentialServices`. A disallowed identity has
  no credential services.

### AgentPolicy status

<!-- generated: agentpolicy-status -->

| Field | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `acceptedGeneration` | integer |  | Minimum 0. | AcceptedGeneration is the latest metadata.generation that the operator accepted. It keeps that acceptance across later transient or registration-only failures of the same generation, so that the operator can tell a refusal caused only by the platform registration from a definitive refusal. It never permits execution: an Agent runs only when the Accepted condition is True for the current generation. |
| `conditions` | []Condition |  | Unique by `type`. | Condition `Accepted` for the configuration of the policy. |
| `observedGeneration` | integer |  | Minimum 0. | Last generation that the controller assessed. |

<!-- /generated: agentpolicy-status -->

The AgentPolicy controller sets the condition `Accepted` for the configuration
of the policy. Reason `PolicyAccepted` means that the platform supports the
controls of the policy and its requested role approvals. It does not assess
Sandbox compatibility, HOME access, or per-Agent enforcement. Other reasons are
`InvalidConfiguration`, `UnsupportedControl`, `UnapprovedGrant`,
`PolicyConflict`, `PrerequisitesUnavailable`, and `Terminating`. If the
operator knows a fixed reason, the message names it, for example a rule budget
that the policy exceeds. The message contains only text from the operator
code, without values from the policy or the registration.

### Identity and network controls

`agentIdentity` and `entraID` are independent. `agentIdentity` permits the
Microsoft Entra directory identity that the Broker and Connections use.
`entraID.enabled` projects an exchange token and the Azure SDK environment into
the main application container with an explicitly approved identity. It needs
platform registration, a pinned ServiceAccount, federation, and resource
permissions outside the cluster. It does not create the directory identity or
grant Kubernetes access. Common-executor Entra projection needs explicit
platform approval and a pinned ServiceAccount that bootstrap owns.

New common-executor Kata OpenClaw and Hermes Agents can use explicitly
registered Kubernetes access with exact policy-role and per-Agent
ServiceAccount approvals. The operator projects a bounded, audience-specific
token only into the main container. It pins the UID, the version, and the rules
of each role, and it records the UIDs of the bindings that it owns.

Suspension, grant removal, and deletion revoke these bindings. The operator does not adopt
or delete shared ClusterRoles. Without `kubernetesAccess`, an Agent gets no
Kubernetes API token or permissions. Kubernetes access does not enable Entra ID,
the Broker, or Connections. See the
[administrator setup]({{< repo "examples/native-sandbox/README.md" >}}#kubernetes-api-access).

With the explicit platform registration and approvals, new common-executor Kata
lifetimes support internal CIDR grants, approved Kubernetes access, and SCTP
port rules. SCTP internal grants are limited to direct endpoints. Registered
Service and Kubernetes API destinations are excluded. Retained version-1 native
single-start lifetimes reject internal grants, Kubernetes access, Entra
projection, and SCTP requests. See the
[native SCTP boundary]({{< repo "examples/native-sandbox/README.md" >}}#governed-sctp-egress).
An accepted API value does not qualify the actual network path.

Observed-SNI filtering with `blockedFQDNs` needs separately registered,
controlled Envoy and trusted capture-init images. It is not full hostname
enforcement: after the explicit inspection window of one second, the connection
continues without inspection. Ingress mTLS needs the separate
[administrator-registered managed endpoint](../../guides/agent-ingress-mtls/).

For the aggregate `PolicyApplied`, Broker-backed Agent Identity needs current
credential-policy readback, and common-executor workloads also need the
registered native Connections integration. Other controls depend on the
selected execution profile and the platform capabilities.

### AgentPolicy model access

| Field | Contract |
| --- | --- |
| `modelAccess.sharedModels.allowed` | Exact Gateway model names that the Agents of the policy can use, routers included. For a router, also list the models of its tiers, its default model, and its context compaction model, if it has one. Each name has at most 256 characters, without whitespace |
| `modelAccess.copilot.allowed` | Boolean. Lets the Agents use their owners' GitHub Copilot. The Portal shows it as **Allow GitHub Copilot** |
| `modelAccess.gatewayBudget.defaultMonthlyUSD`, `maxMonthlyUSD` | Monthly budgets in US dollars, as decimal strings above zero with at most six decimals. Necessary when the policy allows a shared model or Copilot. Not allowed otherwise. The default budget must not exceed the maximum |
| `modelAccess.rateLimits` | Request limits on the key of each Agent. See [AgentPolicy rate limits](#agentpolicy-rate-limits) |

The key of each Agent gets the default budget. An administrator can set the
`modelAccess.monthlyBudgetUSD` of a suspended Agent, up to the maximum. A
budget resets on the first day of each month, UTC. A background job of the
Gateway applies the reset, so it can occur up to about ten minutes late.

When the spend of a key reaches its budget, the Gateway refuses the requests of
the key with HTTP 402 until the reset. The requests to the Agent's GitHub
Copilot models count against the same budget. See
[When the budget runs out](../../guides/models/#when-the-budget-runs-out).

### AgentPolicy rate limits

`modelAccess.rateLimits` sets AI Gateway limits on the virtual key of every
policy-bound Agent whose Team uses the policy. The key of each Agent keeps its
own counters. As a result, Agents do not use up the limits of other Agents.
Every field is optional, and an omitted field is unlimited. There is no zero
value. The Gateway rejects every request of a key whose limit is zero, so the
schema rejects values below the minimums.

| Field | Minimum | Gateway key setting |
| --- | --- | --- |
| `maxParallelRequests` | 1 | `max_parallel_requests`, requests in flight at the same time |
| `rpm` | 1 | `rpm_limit`, requests per minute across all models |
| `tpm` | 4096 | `tpm_limit`, tokens per minute across all models |
| `perModel.<model>.rpm` | 1 | `model_rpm_limit` in the key metadata |
| `perModel.<model>.tpm` | 4096 | `model_tpm_limit` in the key metadata |

`perModel` limits at most 64 models. Its keys are the exact Gateway model names
that the Agent requests (1 to 256 characters without whitespace). Each entry
sets `rpm`, `tpm`, or both. `maxParallelRequests` is at most 2,147,483,647,
which is the largest value that the Gateway stores. The other limits have no
upper bound. The values are not compared with the context window of a model.

For a router of the Agent's Team, a `perModel` key can also be the name of the
router. The operator then also sets the limit under the internal name of the
router, which the Gateway counts for the router. The limit under the name of
the router stays. It applies to another model that keeps the name, for example
a GitHub Copilot model of the Agent. If the policy sets a limit for both names,
the internal name gets the smaller value.

The operator sets the limits when it creates a key. On every reconciliation, it
compares the stored limits of the key with the policy. When they are different,
it updates the same key in place. A removed field is cleared on the key. Other
key settings and metadata are kept.

The limits are not part of the Gateway policy revision. As a result, a change to
them does not start the reprojection fence that a model or budget change starts.
For the same reason, the operator keeps the key of each Agent.

Sandbox execution also excludes `modelAccess.rateLimits` from its execution
inputs and from coordinated configuration reservations. A change that only
adds, edits, or removes these limits, `perModel` included, does not stop or
restart running native Agents. For common-executor Sandboxes, this also applies
to workloads during initialization or during the completion of a retained
restart. A limits-only update does not apply the workload again, block its key
for a short time, or release its unchanged native reservation.

During bootstrap, the operator compares a policy status update with the
captured execution inputs and the immutable UID, not only with its resource
version. Until the policy status reaches the new generation, the operator
assesses the current policy again. It uses the same trusted platform inventory
and grants as the policy controller. It does not stop an Agent only because of
that delay. Policy evidence that is unavailable or not valid holds execution.

Budgets, model authorization, and other execution controls keep their normal
fencing and coordination checks. The exact coordinated write still includes the
limits. The keys of standard Agents do not change. Gateway
administrators manage their limits.

The operator does not rewrite persisted execution evidence. The saved policy
digest of an older single-start lifetime or coordination reservation can include
`modelAccess.rateLimits`, also as an empty object. In that case, the lifetime or
reservation stays fail-closed on an input mismatch, and the stop and recreate
procedure is necessary. Before you edit limits, upgrade the operator and the
admission readers. Do not manually change their saved digests.

The total limits count every request on the key, the Agent's GitHub Copilot
models included. A per-model limit applies to the model name that a request
uses after model aliases. For an `auto` router, that is the name of the router,
not the model that it routes to. For a router of a Team, that is the internal
name of the router, also when the Agent sends the router's name.

A request over a limit gets HTTP 429 with a `retry-after` header. Gateway
replicas share the counters through Redis.

The Gateway counts `tpm` from the tokens of each finished request. Cached input
tokens are excluded. When the count for the minute reaches the limit, the
Gateway refuses the requests of the key until the next minute. A single request
can take the count past the limit. That is the Gateway chart's default,
`tpmReservation: false`.

With `tpmReservation: true`, LiteLLM reserves the input of a request and its
maximum output tokens before it calls the model. If they do not fit, LiteLLM
refuses the request. With every request, OpenClaw asks for the maximum output
of the model, 128,000 tokens for current GPT models. As a result, in that mode,
a `tpm` of 200,000 refuses every request of an OpenClaw conversation after
about 55,000 input tokens. The reservation alone is larger than the limit.
OpenClaw then stops after nine retries of one minute each.

The minimum of 4,096 tokens per minute makes sure that a limit does not refuse
all requests except the smallest.

### Policy binding and execution

Policy assignment is optional for an Agent without a Sandbox selection or a
recorded governed lifetime. Such a standard Agent can use no Team, or a Team
without policy settings. It keeps the standard runtime flow and does not claim
policy enforcement. For a policy-bound Agent, the controller resolves the owner and
Team authorization, the effective AgentPolicy, and the selected Sandbox before
a workload can run. If the authorization is not valid or not available, the
controller holds the Agent.

The native Sandbox path compiles and stages protected resources at zero
replicas. Execution stays held until the Operator's
`sandboxSources.nativeExecution` is enabled and its prerequisites pass. This
setting is off by default. Approved Gateway-backed Custom images use the same
common executor, and the
[Custom image and interface contract](../agent-harness/#policy-governed-custom-execution)
applies to them. If an approval is absent or a combination is not supported,
the Agent stays held. Custom Agents without a Sandbox keep their runtime
provider path.

Before the controller changes a workload, it records a hold finalizer, so a
failed status update cannot release the recorded hold. A suspended Agent stays
`Suspended` unless the shutdown fails. The controller does not change built-in
resources whose Agent-UID ownership it cannot verify, and recovery is necessary
for them. Equal resource names are not sufficient as proof of ownership.

When a held built-in Agent is deleted, the controller removes only the
namespace that it confirms as its own, without Helm uninstall hooks. It keeps
the finalizers until the cleanup completes. A hold keeps the HOME storage, but
an explicit deletion of the namespace removes it. The Agent Identity
authorization and directory cleanup must complete before a held built-in Agent
is deleted, also when the runtime namespace is absent.

Compatibility does not automatically release an old Agent that a mandatory
policy version holds. Before recovery, examine its ownership and the data that
it keeps. If you remove spec fields, this does not clear its recorded policy
lifetime.

The creator of an Agent selects a Team directly. Admission applies the default
Sandbox of the Team. Then it stores references that include the UIDs, so that a
replacement resource with the same name is not adopted.

Only an admission administrator can move a Sandbox Agent to another Team or
Sandbox. In the same update as the new `spec.team` and `spec.sandboxRef`, the
administrator writes the annotation `cetirun.microsoft.com/sandbox-reauthorize`.
Its value names a new request ID and the UIDs of the target binding. These are
the UIDs of the Agent, the Team in the new `spec.team`, the Sandbox in the new
`spec.sandboxRef`, and the AgentPolicy of that Team:

```json
{"requestID": "<new ID>", "binding": {"agentUID": "<UID>", "teamUID": "<UID>", "sandboxUID": "<UID>", "agentPolicyUID": "<UID>"}}
```

Admission rejects the request if a UID is not the UID of the target binding,
for example the UID of the Team before the move. It also rejects a request ID
that is the same as before. After the reauthorization, the Agent stays held.
Admission requires a suspended Agent only for a change of
`modelAccess.monthlyBudgetUSD`. Before a move, suspend the Agent, so that its
workload stops in a controlled way. See the
[reauthorization protocol]({{< repo "app/cetirun-operator/README.md" >}}#direct-kubernetes-writer-assignments-and-explicit-reauthorization).

A policy-bound resource that exists enters a controlled hold for cleanup. A
change or removal of its references does not return it to legacy execution. A
change needs an explicit reauthorization and an ordered stop and cleanup. There
is no lossless rolling migration between policies. The operator status reports
the desired revision of each domain. The operator sets `PolicyApplied` only
when the matching enforcement evidence is current.

AgentPolicy also allowlists Harnesses, skills, exact Gateway model names,
Copilot, and Agent Identity. A native LiteLLM default and maximum monthly USD
budget are necessary for a model entitlement. For Custom, the common executor
and an approved Gateway-qualified image are also necessary. A policy permission
alone does not qualify an image or a backend. Agent Identity cannot complete
the aggregate `PolicyApplied` until a typed credential-service executor
supplies matching read-back evidence.

`skills.allowed` lists the exact entries that the Team's `skills` and an
Agent's `skillOverrides.add` can contain, `npm:` packages included. If a Team
or an Agent requests a skill that the list does not contain, the operator
denies the Agent. Its `PolicyResolved` condition is `False`, with reason
`PolicyDenied` and the message `skill "<name>" is not allowed by policy`. The
operator then revokes the Agent's Gateway key and stops the Agent.

The skill allowlist is not an execution input of Sandbox Agents. A change that
only adds or removes allowed skills does not restart running native Agents. A
policy with an empty or absent allowlist keeps the execution digest that
earlier operators recorded. For a policy that allows skills, the digest that
earlier operators recorded includes the allowlist. After the operator upgrade,
the first reconciliation sees that policy as changed, as after an edit of its
execution settings.

## Portal administration

The Portal writes Teams, Sandboxes, AgentPolicies, and Agents through these
same resources. For its administration pages and HTTP endpoints, see
[Portal API](../portal-api/).

## Schema and implementation

API validation uses these schemas:

- [Agent CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_agents.yaml" >}})
- [Team CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_teams.yaml" >}})
- [Sandbox CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_sandboxes.yaml" >}})
- [AgentPolicy CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_agentpolicies.yaml" >}})
- [Go API types]({{< repo "app/cetirun-operator/api/v1alpha1" "tree" >}})
- [Reconciler and status writes]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
- [Helm value and route builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
- [Portal name validation]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}})
