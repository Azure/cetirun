---
title: "Configure a team"
description: "Apply shared context, policies, and skills with Team."
weight: 50
---

A `Team` is a CetiRun configuration resource.
Both the team and its member instances are cluster-scoped.
It does not define a membership roster, validate Entra group membership, or
authorize access to another user's instance. Portal administrators can manage
teams. Instance owners can select a team.

In the Portal, first create an **Agent Policy** and a **Sandbox** under
**Admin**. A Sandbox selects these items:

- Kata (the default), gVisor, or ordinary Container
- CPU, memory, and ephemeral-storage requests and limits for the Agent and
  each helper
- A HOME StorageClass and size
- A scratch size limit

The runtime and StorageClass must already be available. The creation of this
configuration does not install them.
Container shares the node kernel. It does not give Kata VM isolation. For
Container, an explicitly registered RuntimeClass/handler and the common
AgentPolicy execution path are necessary. Container does not bypass policy or
HOME ownership.

Then select **Admin → Teams → New Team**. Select the Agent Policy, one or more
allowed Sandboxes, and a default Sandbox from that set. Portal-created Teams
must have all three bindings. The editor links to prerequisites that do not
exist. When the cluster rejects a selection, the editor shows configuration
admission errors. The selected Policy must allow the Team Skills.

The Agent Policy can also limit the AI Gateway traffic of each Agent:

- Parallel requests
- Requests per minute (RPM)
- Tokens per minute (TPM)
- RPM or TPM for individual models

Every Agent of a Team that uses the Policy gets the same limits on its own
Gateway key. As a result, the traffic of one Agent does not count against
another Agent. To set no limit, leave a field empty.

When only the limits change, the operator applies the change to the key that
each Agent already has. It does not restart running Agents. Changes to budget,
model access, and Sandbox controls keep their normal enforcement checks. Owners
see the limits in the Agent creation form and on the Agent's page. TPM counts
the tokens of finished requests. See [AgentPolicy rate limits](../../reference/crds/#agentpolicy-rate-limits).

Updates and deletions include the resource UID and version that the editor
read. If another administrator changed or replaced the resource, reload it
before you submit again. Deployments with configuration coordination use the
same protected update/delete flow as Agent Policy administration. Before a
binding can change, it can be necessary to suspend the Agent consumers that
exist.

If an update succeeds in the cluster but its response or coordination release
fails, reload the editor. Then save again. Before the Portal handles the save,
it compares the recorded operation with the current resource. This also
applies to a save with an unchanged configuration. The Portal releases the
reservation only after it finds the exact committed content at a newer version
of the same resource UID.

This recovery works across Portal restarts and never replays the original
write. A stale save still returns a version conflict. An uncertain outcome
keeps its reservation.

If an update stops before submission and its operation is still `Reserved`,
retry the same configuration. Use the original resource UID and version. Only
that exact request can resume the reservation and submit the update. A changed
request or an unchanged save cannot replace or discard the reserved operation.

When the cluster rejects a Team create or update, the Portal shows known field
paths and validation reasons. Before you retry, correct the fields that the
Portal shows. Unexpected server failures keep a generic message.
Administrators can examine the Portal logs for details.

The equivalent configuration is:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: platform
spec:
  agentPolicyRef: standard-policy
  sandboxRefs:
    - standard
  defaultSandboxRef: standard
  memberContext:
    role: Engineer
    description:
      - Review changes before applying them.
```

An instance references the team with `spec.team: platform`. The team must
exist. A reference to a team that does not exist produces `TeamNotFound` on
the instance. Team resource changes enqueue the instances that reference the
team and change their configuration hash. This can trigger Helm upgrades and
pod rollouts.

Hermes refreshes `.hermes/memories/TEAM.md` at initialization. OpenClaw renders
the context into its initial `USER.md`, but keeps that file after home
initialization. A change to a team does not continuously rewrite the user
memory of every agent. See [runtime file updates](../../concepts/configuration/#runtime-files-and-updates).

Add `spec.skills` for approved identifiers and `spec.skillStorage` for a
reviewed shared Blob container. See [configuration and skills](../../concepts/configuration/)
for installation limits and [CRD reference](../../reference/crds/) for the field
names. Install the external dependencies of each skill separately. Also
validate them separately. The shared storage account/container and the CSI
prerequisites must already exist. The team resource does not provision cloud
storage.

The team supplies context, policy, and skills. It does not select a model
provider or hold API credentials. Model access is configured through the AI
Gateway. The effective skill IDs are `(team skills ∪ add) − remove`. Removal
wins, but it does not enforce a prohibition against separate Helm
configuration or user-installed files.

When you apply a team or edit its context, this triggers reconciliation of
the member instances. Before you apply a change, review the affected users.
The deletion of a referenced team is not a cascade-delete or a safe detach.
Before you delete a team, move the references deliberately. Configuration
admission continues to enforce its consumer and cleanup rules.

The Team controller reports if configuration references are accepted. For
runtime readiness, examine the conditions and the effective configuration of
each member instance. Accepted Team configuration is not runtime enforcement.

## Smart routing

A Team can define up to ten smart-routing models (`spec.modelRouters`). Each
is a model name, such as `auto`, `auto-gpt` or `auto-claude`, that the Team's
instances can select like any other model. For each request, the AI Gateway
classifies how difficult the request is and sends it to the model of that
tier.

Administrators define routers on the Team page in the Portal, under
**Smart routing**. Members see only if they can use a router name. They never
see the configuration of the router.

CetiRun does not manage routers made in the Gateway's own console. The routers
of the Gateway chart's `models` stay as they are. The operator gives those
routers to instances under the same rule as a Team's routers. See
[Choose a model](../models/#models-through-the-ai-gateway).

Each router has:

- A **name** of 1 to 64 characters, without spaces or slashes. The name does
  not start with the reserved `cetirun-` prefix, and it is not the name of a
  shared model.
- A **classifier**. `heuristic` (the default) scores each request locally
  without a model call. `llm` asks a shared chat model to classify each
  request. `jev` asks a shared decision model, such as Microsoft-Decision-1, to
  select the tier.

  A decision model is a shared model whose `model_info.mode` is `evaluation`.
  LiteLLM calls it through its Decisions API, and it answers no conversation.
  The calls of an `llm` or `jev` classifier are charged to the instance.

  The classifier is a model, never a router of the Gateway such as `auto`. The
  Gateway makes the classifier call on the instance's behalf without the
  checks of the instance's key. If the classifier is a router, the router
  forwards the call to a model of its own choice.
- Four **tiers**, `simple`, `medium`, `complex` and `reasoning`. Each tier
  names a shared model that answers conversations and is not itself a router,
  or `copilot/<model>` for a GitHub Copilot model. You can leave `reasoning`
  empty to use the `complex` model. Requests that the router cannot classify
  go to the `medium` model.

If the model of a `copilot/<model>` tier is the same model as a shared model,
the tier is registered as that shared model. GitHub Copilot routes of that
model join this shared model (see [Models](../models/#models-through-the-ai-gateway)).
To be granted the router, an instance must have that shared model, and its
AgentPolicy must allow that model. The shared deployments can then also answer
the requests of the tier. A tier that is registered under the Copilot model's
ID is granted only to instances whose owner's Copilot model has that name.

```yaml
spec:
  modelRouters:
    - name: auto
      tiers:
        simple: small-model
        medium: medium-model
        complex: large-model
    - name: auto-copilot
      classifier:
        type: llm
        model: small-model
      tiers:
        simple: copilot/fast-model
        medium: medium-model
        complex: copilot/large-model
        reasoning: large-model
    - name: auto-decision
      classifier:
        type: jev
        model: decision-model
      tiers:
        simple: small-model
        medium: medium-model
        complex: large-model
```

LiteLLM sends a `jev` classifier the instance's whole system prompt and its
latest user messages. Microsoft-Decision-1 takes at most 32,768 tokens. The
AI Gateway shortens a longer classifier request to as many bytes as the
decision model takes tokens. This number is its `model_info.max_input_tokens`,
or 32,768 when that is not set. If the newest user message fits, the Gateway
keeps it whole. It also keeps the start and the end of the rest.

When a classification fails anyway, the router sends the request to its
`medium` model. For example, this occurs while the decision model is
unavailable. The response header `x-litellm-complexity-router-cause` and the
request log's `routing_cause` show `jev_classifier`. After a failed
classification, they show `default_model_fallback`.

The operator registers each router in the AI Gateway under an internal name
and reports it on the Team. `status.modelRouters` lists each router as ready,
or shows why it is not ready. The `ModelRoutersReady` condition summarizes
them.

A router is not registered while any of these conditions is true:

- A tier or the classifier names a model that the Gateway does not offer as a
  shared model.
- A tier or the classifier names a router of the Gateway.
- A tier or an `llm` classifier names a shared model that answers no
  conversation.
- A `jev` classifier names a shared model that is not a decision model.

The message names the router and the tier or the classifier. A changed router
replaces its registration under the same internal name. As a result,
instances keep their configuration. A removed router is unregistered.

The registrations appear in the Gateway's console under their internal
`cetirun-router-` names. Change them on the Team, not in the console. The
operator does not watch the console for edits. The Gateway's request log also
shows a Team's router under that internal name. The Log Analytics queries in
the Gateway chart's
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers)
section also show the router under that name. The Team's
`status.modelRouters[].gatewayModel` gives the router that each name belongs
to.

The key of each instance that can use a router also has an alias for it: the
router's name for its internal name. As a result, the instance can send
requests to the router by its name. The model list of the key
(`GET /v1/models`) shows the router by its name only. A Hermes instance is
configured with the router under its name, which Hermes shows in its model
picker. An OpenClaw instance keeps the internal name, and shows the router's
name. If the instance reaches another model by the router's name, for example
one of its GitHub Copilot models, that model keeps the name. Then the instance
uses the router by its internal name.

An instance gets a router only when it can use every model that the router
sends requests to. The Gateway also enforces this rule for the router. If a
router sends a request to a model outside the instance's key, the Gateway
refuses the request with HTTP 403. As a result:

- With an Agent Policy, the Policy's allowed shared models must list the
  router's name and each shared tier. For GitHub Copilot tiers, Copilot must
  be allowed. The classifier must be a shared model, a chat model for `llm` or
  a decision model for `jev`. But it is not necessary to list the classifier.
- For a router with a shared tier or an `llm` or `jev` classifier, the
  instance must use shared models.
- A GitHub Copilot tier works for an instance after its owner signs in to
  GitHub Copilot with an account that offers that model. The account of every
  member is expected to offer the same models. A router whose tiers are all
  shared models works immediately. A Copilot tier uses only the instance's own
  sign-in. If its model is not available to the instance when a request
  arrives, the Gateway sends the request to the router's medium model. For
  example, this occurs immediately after a sign-out and before the operator
  withdraws the router.

An instance that cannot use a router reports the reason in
`status.aiGateway.modelRouters`, and its Portal page shows the reason. A
change to the tiers or the classifier of a router updates instance keys and
registrations. It does not change the Policy's Gateway projection. Only the
limits that the instance is configured with for the router follow the tiers,
as described in [Choose a model](../models/#smart-routing-models).

Without a LiteLLM license, LiteLLM limits some router features to a single
router per Gateway. These features are:

- The `heuristic_v2`, `capability` and `llm_v2` classifiers
- Custom tier sets
- Custom classifier prompts, which include the instructions of a `jev`
  classifier

A Team cannot use these features. The Gateway chart's own routers can use
them, within that limit. A Team's `jev` classifier keeps LiteLLM's own
instructions, which LiteLLM does not limit.

Sources: [team event handling]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[team defaults and skills]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[team model routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_routers.go" >}}),
[admin team handlers]({{< repo "app/cetirun-portal/internal/handler/admin_team.go" >}}),
and [runtime initialization]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-init-home.sh" >}}).
