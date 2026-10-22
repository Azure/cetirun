---
title: "Configure a team"
description: "Apply shared context and provider defaults with Team."
weight: 50
---

A `Team` is a CetiRun configuration resource, not a Microsoft Teams bot.
Both the team and its member instances are cluster-scoped.
It does not define a membership roster, verify Entra group membership, or
authorize access to another user's instance. Portal administrators can manage
teams; instance owners can select a team.

In the Portal, create an **Agent Policy** and a **Sandbox** under **Admin**
first. A Sandbox selects Kata or gVisor, CPU/memory/ephemeral-storage requests
and limits for the Agent and each helper, a HOME StorageClass and size, and a
scratch size limit. The runtime and StorageClass must already be available;
creating this configuration does not install them.

Then choose **Admin → Teams → New Team**. Select the Agent Policy, one or more
allowed Sandboxes, and a default Sandbox from that set. Portal-created Teams
require all three bindings. The editor links to missing prerequisites and
shows configuration admission errors when the cluster rejects a selection.
Team Skills must be allowed by the selected Policy.

Updates and deletions include the resource UID and version that the editor
read. If another administrator changed or replaced the resource, reload it
before submitting again. Deployments with configuration coordination use the
same protected update/delete flow as Agent Policy administration. Existing
Agent consumers may need to be suspended before a binding can change.

If an update succeeds in the cluster but its response or coordination release
fails, reload the editor and save again. Portal checks the recorded operation
against the current resource before handling the save, including an unchanged
configuration. It releases the reservation only after observing the exact
committed content at a newer version of the same resource UID. This recovery
works across Portal restarts and never replays the original write. A stale save
still returns a version conflict; an uncertain outcome retains its reservation.

If an update stops before submission while its operation is still `Reserved`,
retry the same configuration with the original resource UID and version. Only
that exact request can resume the reservation and submit the update. A changed
request or an unchanged save cannot replace or discard the reserved operation.

When the cluster rejects a Team create or update, the Portal shows known field
paths and validation reasons. Correct the indicated fields before retrying.
Unexpected server failures retain a generic message; administrators can inspect
Portal logs for details.

The corresponding configuration is:

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
  defaults:
    provider: github_copilot
```

An instance references the team with `spec.team: platform`. The team must exist:
a missing reference produces `TeamNotFound` on the instance. Team resource
changes enqueue referencing instances and change their configuration hash,
which can trigger Helm upgrades and pod rollouts.

Hermes refreshes `.hermes/memories/TEAM.md` at initialization. OpenClaw renders
the context into its initial `USER.md`, but preserves that file after home
initialization. Changing a team does not continuously rewrite every agent's
user memory. See [runtime file updates](../../concepts/configuration/#runtime-files-and-updates).

Add `spec.skills` for approved identifiers and `spec.skillStorage` for a
reviewed shared Blob container. See [configuration and skills](../../concepts/configuration/)
for installation limits and [CRD reference](../../reference/crds/) for the field
names. Install and verify each skill's external dependencies separately.
The shared storage account/container and CSI prerequisites must already exist;
the team resource does not provision cloud storage.

The team supplies a provider, not a model ID or API credential. Instance Helm
provider settings take precedence, and OpenAI/Anthropic need an explicit model
and separately prepared credentials. Effective skill IDs are
`(team skills ∪ add) − remove`; removal wins, but does not enforce a prohibition
against separate Helm configuration or user-installed files.

Applying a team or editing its provider/context triggers reconciliation of
member instances. Review the affected users before applying a change.
Deleting a referenced team is not a cascade-delete or safe detach. Move
references deliberately before deleting a team; configuration admission
continues to enforce its consumer and cleanup requirements.

The Team controller reports whether configuration references are accepted.
Inspect each member instance's conditions and effective configuration for
runtime readiness; accepted Team configuration is not runtime enforcement.

## Smart routing

A Team can define up to ten smart-routing models (`spec.modelRouters`). Each is
a model name, such as `auto`, `auto-gpt` or `auto-claude`, that the Team's
instances can choose like any other model. For each request, the AI Gateway
classifies how demanding the request is and sends it to the model of that
tier. Administrators define routers on the Team page in the Portal, under
**Smart routing**; members see only whether they may use a router name, never
its configuration. Routers made in the Gateway's own console are not managed
by CetiRun, and the routers of the Gateway chart's `models` stay as they are;
the operator gives those to instances under the same rule as a Team's routers,
see [Choose a model](../models/#models-through-the-ai-gateway).

Each router has:

- a **name**, 1 to 64 characters without spaces or slashes, not starting with
  the reserved `cetirun-` prefix, and not the name of a shared model;
- a **classifier**: `heuristic` (the default) scores each request locally
  without a model call; `llm` asks a shared model to classify each request,
  and those calls are charged to the instance. The classifier is a model, never
  a router of the Gateway such as `auto`: the Gateway makes the classifier call
  on the instance's behalf without its key's checks, and a router would send it
  on to a model of its own choice;
- four **tiers**, `simple`, `medium`, `complex` and `reasoning`, each naming a
  shared model that is not itself a router, or `copilot/<model>` for a GitHub
  Copilot model. `reasoning`
  may be left empty to use the `complex` model. Requests the router cannot
  classify go to the `medium` model.

A `copilot/<model>` tier whose model is the same model as a shared model is
registered as that shared model, which GitHub Copilot routes of that model
join (see [Models](../models/#models-through-the-ai-gateway)): an instance needs that shared model, allowed by
its AgentPolicy, to be granted the router, and the tier's requests may then be
answered by the shared deployments too. A tier registered under the Copilot
model's ID is granted only to instances whose owner's Copilot model has that
name.

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
```

The operator registers each router in the AI Gateway under an internal name
and reports it on the Team: `status.modelRouters` lists each router as ready or
explains why it is not, and the `ModelRoutersReady` condition summarizes them.
A router is not registered while a tier or the classifier names a model the
Gateway does not offer as a shared model, or a router of the Gateway; the
message names the router and the tier. A changed router replaces its registration under the same internal name,
so instances keep their configuration, and a removed router is unregistered.
The registrations appear in the Gateway's console under their internal
`cetirun-router-` names; change them on the Team, not in the console, which
the operator does not watch for edits. The Gateway's request log, and the
Log Analytics queries in the Gateway chart's
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers)
section, show a Team's router under that internal name too; the Team's
`status.modelRouters[].gatewayModel` gives the router each name belongs to.

An instance gets a router only when it may use every model the router sends
requests to, which the Gateway also holds the router to: it refuses, with HTTP
403, a request a router sends to a model outside the instance's key. So:

- with an Agent Policy, the Policy's allowed shared models must list the
  router's name and each shared tier, and GitHub Copilot tiers need Copilot to
  be allowed. The classifier must be a shared model, but need not be listed;
- the instance must use shared models for a router with a shared tier or a
  model classifier;
- a GitHub Copilot tier works for an instance once its owner has signed in to
  GitHub Copilot with an account that offers that model. Every member's
  account is assumed to offer the same models; a router whose tiers are all
  shared models works at once. A Copilot tier only ever uses the instance's
  own sign-in: if its model is not available to the instance when a request
  arrives, for example just after a sign-out and before the operator withdraws
  the router, the Gateway sends the request to the router's medium model.

An instance that cannot use a router reports why in
`status.aiGateway.modelRouters`, and its Portal page shows the reason. Changing
a router's tiers or classifier updates instance keys and registrations; it
does not change the Policy's Gateway projection. Only the limits the instance
is configured with for the router follow the tiers, as described in
[Choose a model](../models/#smart-routing-models).

LiteLLM limits some router features to a single router per Gateway without a
LiteLLM licence: the `heuristic_v2`, `capability` and `llm_v2` classifiers,
custom tier sets and custom classifier prompts. A Team cannot use them; the
Gateway chart's own routers can, within that limit.

Sources: [team event handling]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[team defaults and skills]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[team model routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_routers.go" >}}),
[admin team handlers]({{< repo "app/cetirun-portal/internal/handler/admin_team.go" >}}),
and [runtime initialization]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-init-home.sh" >}}).
