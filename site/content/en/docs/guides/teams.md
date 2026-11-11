---
title: "Configure a team"
description: "Apply shared context, policies, and skills with Team."
weight: 50
---

A `Team` is a CetiRun configuration resource.
Both the team and its member instances are cluster-scoped.
It does not define a membership roster, validate Entra group membership, or
authorize access to another user's instance. Portal administrators can manage
teams.

Every signed-in Portal user can select any Team, or no Team, for a new Agent.
An Agent Policy applies only to the Agents of a Team that has one, so it is not
an access boundary between users. Refer to
[Policy-bound Teams](../../operations/policy-teams/).

In the Portal, first create an **Agent Policy** and a **Sandbox** under
**Admin**. A Sandbox selects these items:

- The isolation type, which the form labels **Runtime**: Kata (the default) or
  Container. The form also offers gVisor, but Sandbox execution does not
  support it, and the Portal does not save a gVisor Sandbox.
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

The Agent Policy editor offers more controls than a platform registration can
support. Before you use a control, make sure that the registration supports
it. Otherwise, the Team becomes incompatible. Refer to
[Policy settings and registration capabilities](../../operations/policy-teams/#policy-settings-and-registration-capabilities).

Then select **Admin → Teams → New Team**. Select the Agent Policy, the allowed
Sandboxes, and a default Sandbox from that set. Portal-created Teams must have
all three bindings.

If no Agent Policy or Sandbox exists yet, the editor links to the page that
creates one. A Team can allow more than one Sandbox only when its Policy
enables **Allow multiple Sandboxes per Team**. When the cluster rejects a
selection, the editor shows configuration admission errors. The selected
Policy must allow the Team skills.

The Agent Policy can also limit the AI Gateway traffic of each Agent: parallel
requests, and requests and tokens per minute, also for individual models. Each
Agent gets the limits on its own key. Owners see the limits in the Agent
creation form and on the Agent's page. Refer to
[Request limits](../../operations/budgets/#request-limits). A change to only
the limits does not restart Agents. For the changes that restart them, refer
to [What changes for Agents](../../operations/policy-teams/#what-changes-for-agents).

The Portal saves a Team as [Save changes](../../operations/portal-administration/#save-changes)
shows. Some deployments enable configuration coordination. There, a change to
a binding can require that you suspend the Agents of the Team first.

If a save fails after the cluster accepted it, reload the editor and save
again. The Portal finds the change that the cluster committed, and it does not
write the change two times. This recovery also works after a Portal restart.

With configuration coordination, a save that stops before the Portal sends it
stays reserved. To complete it, save the same configuration again, from the
same version of the Team. A changed request or a save without changes cannot
replace or discard it.

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

The Sandbox Agents of a policy-bound Team do not mount the container that
`spec.skillStorage` names. They receive Team skills from the
[Skill Marketplace](../../operations/skill-marketplace/), or from a published
skills catalog. Refer to
[Skill sources of an Agent](../../operations/skill-marketplace/#skill-sources-of-an-agent).

A Team has at most 100 skill entries. With the
[Skill Marketplace](#skills-from-the-skill-marketplace), the entries without
the `npm:` prefix are the names of marketplace skills.

A Team selects the Agent Policy that decides the models, the budget, and the
request limits of its Agents. It also selects the Sandboxes that run them, its
smart-routing models, the shared context, and the skills. It holds no provider
credentials. Refer to [Model access](../../operations/models/).

The effective skill IDs are `(team skills ∪ add) − remove`. Removal wins, but
it does not enforce a prohibition against separate Helm configuration or
user-installed files.

When you apply a team or edit its context, this triggers reconciliation of
the member instances. Before you apply a change, review the affected users.
The deletion of a referenced team is not a cascade-delete or a safe detach.
Before you delete a team, move its Agents to another Team, as
[Move an Agent to another Team](../../operations/policy-teams/#move-an-agent-to-another-team)
shows. Configuration admission continues to enforce its consumer and cleanup
rules.

The Team controller reports if configuration references are accepted. For
runtime readiness, examine the conditions and the effective configuration of
each member instance. Accepted Team configuration is not runtime enforcement.

## Skills from the Skill Marketplace

When the [Skill Marketplace](../../operations/skill-marketplace/) is enabled,
the Team page in the Portal has a **Skills** section. It lists the skills that
the Skill Marketplace synchronized from its Git repository. Select the skills
that the Team's instances get. The Team's Agent Policy must allow each skill.
A skill that the Policy does not allow shows **Not allowed by the Agent
Policy**.

> **Warning:** Do not enter packages in **npm skill packages, one per line**
> for a Team with an Agent Policy. With an `npm:` entry, the Sandbox Agents of
> the Team do not start. Refer to
> [Limits](../../operations/skill-marketplace/#limits).

Saving changes to Team skills restarts the Team's running Sandbox Agents one
time. Their new Pods copy the Team's published skills. OpenClaw and Hermes
Agents read this copy read-only at `$HOME/.aks/skills`, and Custom Agents in
`$HOME/.agents/skills`. Changes in the skills repository reach an instance at
its next start.

In `spec.skills`, each entry without the `npm:` prefix is then the name of a
marketplace skill. These rules apply:

- The Skill Marketplace delivers skills only to the common-executor Sandbox
  Agents of a Team with an Agent Policy: OpenClaw, Hermes, and Custom.
- An instance cannot add or remove a marketplace skill with `skillOverrides`.
  The operator refuses such an instance with `AgentConfigInvalid`.
- The Portal adds only skills that the Skill Marketplace catalog has. A Team
  can keep a selected skill after the skill leaves the repository. In that
  case, the Team's Agents keep its last published version.
- The Portal does not let a Policy save remove a marketplace skill that a
  Team of the Policy still selects.

## Smart routing

A Team can define up to ten smart-routing models (`spec.modelRouters`). Each
is a model name, such as `auto-team`, `auto-gpt` or `auto-claude`, that the
Team's instances can select like any other model. For each request, the AI
Gateway classifies how difficult the request is and sends it to the model of
that tier.

Administrators define routers on the Team page in the Portal, under
**Smart routing**. Members see only if they can use a router name. They never
see the configuration of the router.

CetiRun does not manage routers made in the Gateway's own console. The routers
of the Gateway chart's `models` stay as they are. The operator gives those
routers to instances under the same rule as a Team's routers. See
[Model access](../../operations/models/#routers).

Each router has:

- A **name** of 1 to 64 characters, without spaces or slashes. The name does
  not start with the reserved `cetirun-` prefix, and it is not the name of a
  shared model. With the default `modelRouting.mode: auto` of `make deploy`,
  `auto` is the name of a shared model, because it is the router of the AI
  Gateway.
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
  names a shared model that answers conversations and is not itself a router.
  A tier can also name `copilot/<model>` for a GitHub Copilot model. You can
  leave `reasoning` empty to use the `complex` model.

  The `medium` model is also the router's default model. For example, a
  request goes to it when its `jev` classification fails. If an `llm`
  classifier fails, the router classifies the request with `heuristic` instead.

If the model of a `copilot/<model>` tier is the same model as a shared model,
the tier is registered as that shared model. GitHub Copilot routes of that
model join this shared model (see [Names of GitHub Copilot models](../../operations/models/#names-of-github-copilot-models)).
To be granted the router, an instance must have that shared model, and its
AgentPolicy must allow that model. The shared deployments can then also answer
the requests of the tier. A tier that is registered under the Copilot model's
ID is granted only to instances whose owner's Copilot model has that name.

```yaml
spec:
  modelRouters:
    - name: auto-team
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
AI Gateway shortens a longer classifier request so that it fits.

The limit in bytes is the number of tokens that the decision model takes, less
the size of LiteLLM's questions and 512 bytes. The number of tokens is the
model's `model_info.max_input_tokens`, or 32,768 when that is not set. If the
newest user message fits, the Gateway keeps it whole. It also keeps the start
and the end of the rest.

When a classification fails anyway, the router sends the request to its
`medium` model. For example, this occurs while the decision model is
unavailable. The response header `x-litellm-complexity-router-cause` and the
request log's `routing_cause` show `jev_classifier`. After a failed
classification, they show `default_model_fallback`, and the request log has no
`routing_tier`.

The operator registers each router in the AI Gateway under an internal name
and reports it on the Team. `status.modelRouters` lists each router as ready,
or shows why it is not ready. The `ModelRoutersReady` condition summarizes
them.

For a ready router, `status.modelRouters[].nonReasoningModels` lists the tier
models that do not reason. The Team page in the Portal shows them with the
router. OpenClaw instances then offer no reasoning levels for the router. The
Gateway removes reasoning settings from the requests that the router sends to a
shared model that does not reason. This list is only a hint. The router is
registered and granted as usual. Refer to
[Reasoning](../models/#reasoning).

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
shows a Team's router under that internal name. The queries in the Gateway
chart's
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
name.

If the instance reaches another model by the router's name, for example one of
its GitHub Copilot models, that model keeps the name. Then the instance uses
the router by its internal name. A per-model limit that the Agent Policy sets
for the name applies to the other model. The operator also sets this limit
under the internal name, for the router.

An instance gets a router only when it can use every model that the router
sends requests to. The Gateway also enforces this rule for the router. If a
router sends a request to a model outside the instance's key, the Gateway
refuses the request with HTTP 403. As a result:

- With an Agent Policy, the Policy's allowed shared models must list the
  router's name and each shared tier. After you save the Team, the router's
  name appears in the **Allowed shared models** picker of the Policy. For
  GitHub Copilot tiers, Copilot must be allowed. The classifier must be a
  shared model, a chat model for `llm` or a decision model for `jev`. But it
  is not necessary to list the classifier.
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
change to the routers of a Team updates the AI Gateway registrations and the
keys of the Team's Agents. It does not change the Policy's Gateway projection. It
restarts an Agent only when the model configuration of the Agent changes. For
example, this occurs when a router is added, or when the limits of its tiers
change, as described in
[Choose a model](../models/#smart-routing-models). Other Team changes, such as
its context or its skills, restart the Team's running Sandbox Agents one time.

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
