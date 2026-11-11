---
title: "Portal API"
description: "The administration pages and HTTP endpoints of the Portal for Teams, Sandboxes, AgentPolicies, the Skill Marketplace, and Agent creation."
weight: 30
---

The Portal writes Teams, Sandboxes, AgentPolicies, and Agents through the same
[custom resources](../crds/) that `kubectl` uses. Its backend serves the HTTP
endpoints in this page, and the Portal web application calls them. Kubernetes
admission examines each write again, and the operator decides if an Agent can
run. A successful Portal save does not prove that the operator applied the
change.

For the private registry endpoints and the Custom self-service request, see
[Private registries](../agent-harness/#private-registries) and
[Portal self-service](../agent-harness/#portal-self-service).

## Version preconditions

Each update or delete of a Team, a Sandbox, or an AgentPolicy sends the `uid`
and the `resourceVersion` that a previous read returned. The responses are
these:

| Request | Response |
| --- | --- |
| Without the version fields | `428 Precondition Required` with the code `version_required` |
| With a stale version | `409 Conflict` |
| Rejected because of a known compatibility problem | `422 configuration_incompatible`, with an optional `error.details` list of field and combination diagnostics |

A Team request should also send the generation that the user reviewed, in
the `X-CetiRun-Team-Generation` header. The API also accepts a JSON
`generation` field. If a request sends both, they must match. These rules
apply:

- With a generation, a status-only update of the Team does not invalidate the
  request. A changed configuration or a replacement Team returns
  `409 Conflict`.
- Without a generation, the exact resource version is necessary, as for older
  clients.
- A generation that is not one positive integer returns `400 Bad Request` with
  the code `invalid_generation`.

The Portal web application sends the header and keeps the JSON body shape.
During a rolling upgrade, older Portal replicas ignore the header and apply the
exact resource-version check. Older replicas reject the JSON `generation`
field, because they reject unknown body fields.

After the Portal checks the reviewed configuration, it uses the latest
resource version for the conditional write. A race during the submission can
still cause a conflict. Uncertain coordinated writes keep their recovery
requirements.

A successful save does not mean that the operator examined the new version.

## Team and Sandbox administration

Portal administrators can create and edit Teams with `agentPolicyRef`,
`sandboxRefs`, and `defaultSandboxRef`. The Portal accepts only complete
bindings and validates the referenced resources. It also makes sure that the
default is in the allowed Sandbox set and that the policy allows the Team
skills. The Team DTO returns these fields together with `uid`,
`resourceVersion`, and `generation`.

The Teams list shows the compatibility of the saved policy and of each
selected Sandbox. Expand a Team to see its overall compatibility, the labeled
Policy and Sandbox references, the reasons, and the check time. Multiple
Sandboxes appear in a table with a separate result for each Policy and Sandbox
combination. For the report fields, the states, and the issue codes, see
[Team compatibility](../crds/#team-compatibility).

The Policy and Sandbox lists also show the Teams that refer to them. The Sandbox
view shows the overall Team result separately from the result of that Sandbox.
The configuration acceptance of a policy and the prerequisites of a Sandbox are
separate from Team compatibility. None of these observations proves that an
Agent applied its policy or is ready to run.

The admin Team API includes the compatibility report, the Team generation, and
the deletion state. The Sandbox API also returns the generation, the observed
generation, and the conditions.

The Team editor keeps unsaved input across background refreshes. If the saved
configuration changes, it keeps the draft and asks you to discard it and
reload before you save. A status-only refresh does not replace the draft. The
Team deletion dialog shows its errors inside the dialog. After a configuration
conflict, reload and review the current Team before you confirm the deletion
again.

Edit pages show field validation and the errors of the current save. They do
not examine unsaved drafts or show the saved status as a draft result.
Kubernetes admission examines the submitted configuration before it stores it.
For a known compatibility rejection, the editor keeps the unsaved input.
Version, permission, and coordination errors stay separate.

The **Admin → Sandboxes** pages manage the complete Sandbox configuration. This
configuration is the runtime type, the resources of the Agent and its helpers,
the HOME storage, and the scratch size. The pages use these admin-only
endpoints:

- `GET` and `POST /api/v1/admin/sandboxes`
- `GET`, `PUT`, and `DELETE /api/v1/admin/sandboxes/{name}`

The Sandbox request contains `name`, an optional `type`, `resources`, and
`storage`, with the same structure as the CRD spec. If `type` is omitted or
`null`, the value is `kata` on both create and full-spec update. This agrees
with how Kubernetes applies defaults. An explicit value that is not valid, for
example an empty string, is rejected and not replaced by the default. The
resource and storage fields are necessary.

The response reports `ready` only from a condition for the current
generation. A save of a Sandbox does not install a runtime or establish runtime
readiness.

When configuration coordination is configured, Team and Sandbox changes use
the coordination client. This includes conditional writes, uncertain outcomes
that the Portal keeps, and background deletion. Admission continues to check
platform compatibility and the current consumers. The editor shows Kubernetes
validation and admission rejections. Transport failures stay in the logs.

## Skill Marketplace administration

The Portal chart's `skillMarketplace.enabled` enables these Portal API
behaviors. Keep it equal to the operator's `skillMarketplace.enabled`.

- `GET /api/features` returns `skillMarketplaceEnabled`.
- `GET /api/v1/admin/skill-marketplace` is for administrators only. It returns
  `enabled`, `known`, and `catalog`, with `Cache-Control: no-store`. `enabled`
  is `false` when the Portal setting is off. `known` is `false` until the
  Skill Marketplace writes its catalog, and `catalog` is then `null`.
- The catalog is the `catalog.json` document of the ConfigMap
  `cetirun-skill-marketplace` in the operator namespace. The Portal accepts
  only the document version `v1`, without duplicate fields, and at most 1 MiB.
  If it cannot read or decode the document, the response is
  `503 Service Unavailable` with the code `skill_marketplace_unavailable`.
- Team creation and update examine `skills`. Each entry without the `npm:`
  prefix must be a valid marketplace skill name. Otherwise, the response is
  `422` with the code `invalid_team`. Each entry must be in the `skills.allowed`
  of the policy (`400`, `invalid_team_binding`).
- Each marketplace skill that the stored Team does not select yet must be in
  the catalog (`422`, `invalid_team`). When the Skill Marketplace rejected the
  copy of the repository, the message gives the reason. Before the catalog
  exists, the response is `422` with `invalid_team`. If the Portal cannot read
  the catalog, the response is `503` with `skill_marketplace_unavailable`. A
  skill that the Team already selects can stay after it leaves the repository.
- A policy update cannot remove a marketplace skill from `skills.allowed`
  while a Team of the policy selects it. The response is `422` with the code
  `invalid_policy`, and the message names the Team.
- `GET /api/v1/instances/{name}` returns `teamSkills` for an Agent whose Team
  has an AgentPolicy and selects marketplace skills. The field is absent when
  the Portal cannot read the Team or the catalog.
- Each entry of `teamSkills` has `name`, `description`, `version`, and `state`.
  The version is the one in the release of the Team. The state is `published`,
  `updating`, `pending`, `retained`, or `missing`. `updating` means that the
  repository has another version, which the entry gives as
  `repositoryVersion`. Without a catalog, each skill is `pending`.
- The same response has `teamSkillsPublicationFailed: true` when the catalog
  has a publication error for the Team. The response does not include the
  error. Administrators see it on **Admin → Skills**.
- The same response has `teamSkillsRestartNeeded: true` when the newest Pod
  of the Agent copied an earlier Team release. Both releases are of the current
  Team selection. The Portal reads the copied release ID from the termination
  message of the `init-home-data` container. Without that ID, the field is
  absent.

See [Skill Marketplace](../../operations/skill-marketplace/) for the catalog
fields and the states.

## AgentPolicy administration

The admin Portal shows the complete `AgentPolicy` configuration.
Administrators can edit the allowed Harnesses and skills, Copilot and shared
model access, the monthly USD budgets, and the per-Agent rate limits. They can
also edit multiple-Sandbox selection, the read-only root filesystem, network
CIDR and port rules, the ingress mode, Agent Identity, and Entra workload
identity. With the Skill Marketplace, the editor also lists the skills of its
catalog, so that administrators can select the allowed skills.

The **Identity** section has two independent controls:

- **Agent Identity** selects `Disabled`, `Optional`, or `Required` for an
  independent Microsoft Entra directory identity that the Broker and
  Connections use. `Optional` lets the user choose during Agent creation.
  `Required` selects it automatically. Both enabled modes set the
  credential-service entitlement `azure-resource-manager`. `Disabled` clears
  that entitlement. The platform must have the
  [Agent Identity setup](../../operations/agent-identity/).
- **Entra workload identity** controls `entraID.enabled`.

Neither control enables the other. New policies have both controls disabled.

The Portal shows Kubernetes API access under **Read-only policy
capabilities**. The summary shows the ServiceAccount and the namespace or
cluster-wide permissions that the policy requests. Platform administrators
manage this configuration outside the Portal. If you save other policy fields,
the save keeps the Kubernetes access configuration.

The **Internal network access** editor puts each destination CIDR and its
complete port list into one rule card. Each card matches one
`allowedInternalCIDRs` entry. **Edit rule** opens the entire rule. **Update
rule** replaces its destination and ports together. **Cancel** discards the
draft. **Delete rule** removes the entire entry.

In the editor, ports are comma-separated, for example `443, 5432, 53/UDP`. A
port uses TCP unless it has `/UDP` or `/SCTP`. The editor submits rule changes
when you save the policy.

Blocked FQDNs and ingress mTLS are read-only in this editor. The values that
exist stay visible, and the editor keeps them when you save other fields. For
the effect of each control, see [AgentPolicy](../crds/#agentpolicy).

The Portal list shows the `Accepted` condition of the policy for the current
generation. The editor shows only field validation and save errors. If you
edit a policy that is already bound, the edit can affect the Agents of its
Teams.

### HTTP API

These admin-only endpoints manage `AgentPolicy` resources:

- `GET` and `POST /api/v1/admin/policies`
- `GET`, `PUT`, and `DELETE /api/v1/admin/policies/{name}`

Update and delete requests include the UID and the resource version that a
previous read returned. See [Version preconditions](#version-preconditions).
Responses include all policy configuration groups, `generation`,
`observedGeneration`, `conditions`, and `deleting`. These status fields are
read-only.

Each create or update replaces `agentIdentity` and `entraID` as complete
objects. If a request omits a group or sends `null`, the group is disabled. An
empty object also uses the disabled defaults. The Portal always sends both
groups.

`agentIdentity.required` needs `allowed: true`. An allowed identity
must include `azure-resource-manager` in `credentialServices`. A disabled
identity has no credential services. Unknown and duplicate services are
rejected. There is no dependency between `agentIdentity` and
`entraID.enabled`.

For Sandbox selection, root filesystem security, and the editable network
fields, an omitted field keeps the stored value. Explicit empty arrays clear
the editable lists. This API cannot change Kubernetes access, blocked FQDNs, or
mTLS. A request that tries to change or clear Kubernetes access is rejected. If
a request omits it, the configuration stays. Updates keep the metadata, the
status, and the coordinated mutation and optimistic concurrency protections.

The API validates model access and budgets as one unit. For Copilot or shared
model access, a positive default and maximum monthly USD budget are necessary.
The default cannot be more than the maximum. The API applies the rate-limit
minimums of the schema before it writes.

`modelAccess.rateLimits` in an update replaces the stored limits. An update
without it removes them. As a result, a client saves the limits that it read.
The Portal editor treats an empty input as no limit and never sends zero.

### Coordinated changes

When strict Sandbox admission uses a UID-pinned configuration authority, set
the Portal chart's `configurationCoordination.namespace`, `name`, and `uid`.
Set them to the same authority that the operator uses. The platform chart
rejects values that do not match.

The Portal then reserves the exact whole-resource mutation. It records
`Changing` before it submits one conditional Update or background Delete, and
then it observes the result. It releases only an operation whose outcome is
confirmed. Its namespaced Role allows access only to the named authority
ConfigMap. The UID and resource-version checks protect against concurrent
requests. They do not replace this admission contract.

A reservation conflict returns `409 Conflict`. An unavailable authority or an
uncertain API write returns `503 Service Unavailable`. In that case, the Portal
keeps the reservation. Do not repeat the mutation. Recover the recorded
operation with the operator's configuration-change procedure.

Update reservations record the digests of the complete writable content before
and after the change. Before a save, the Portal looks for a recorded update,
also after a restart. A read of the same live UID at a different
resourceVersion allows recovery when the content matches the proposed
configuration (applied). It also allows recovery when the content matches the
original configuration (superseded). A status-only refresh can cause a
superseded result, and then the requested edit is not applied.

Other configuration changes or an unknown outcome keep the reservation.
Recovery never submits the old update again, and it never relaxes the version
checks of the caller. A stale edit must get a new read before a save.

CLI update recovery uses the exact original `--before` and `--after` files.
`--action observe` is read-only. It reports
`update-superseded-reservation-retained` when a newer version has only the
original writable content. Explicit `--action update` then releases the old
reservation and reports `update-superseded`. It does not write the
configuration.

New deletion reservations record the content digest that recovery needs. An
administrator can use the operation ID from the pinned authority journal with
`--action observe-delete --operation-id ID`, also after the policy and the
Portal request are gone. Observation keeps the reservation. Explicit
`--action delete --operation-id ID` releases it only after it makes sure that
the original UID is absent or in termination. It does not submit a Delete
again.

For older deletion reservations, the original `--before` input is necessary.
Before you issue reservations with the recovery metadata, upgrade all
coordination readers. Configuration coordination protects updates and
deletions, not creation.

### Agent creation options

The creation form selects a Team before it offers Harness and model choices.
For `GET /api/v1/instance/create-options?team=<name>`, a signed-in user is
necessary. This endpoint returns the selected Team, the policy name when the
Team is bound, the allowed Agent types, and the effective Gateway catalog. It
also shows if Agent Identity is available and if it is necessary. It also
returns the per-Agent rate limits of the policy and any configuration blocker.
Responses use `Cache-Control: no-store`.

This endpoint does not expose other administrative policy fields or grant
runtime authorization. The form and the Agent details page show the rate
limits read-only. The details page reads them from the policy of the Team of
the Agent.

Harness choices are the deployment settings that `harnesses.allowed` also
contains. Shared models are the Gateway catalog entries that
`modelAccess.sharedModels.allowed` also contains. For Copilot, both deployment
availability and policy permission are necessary.

If Agent Identity is necessary, the form selects it, and the user cannot clear
it. Agent Identity also needs the deployment feature, a confirmed owner, and a
credential-service entitlement of the policy. It does not need the
`entraID.enabled` workload projection. If a prerequisite is not met, creation
is blocked.

Both Agent creation endpoints read the same constraints again before they
write an Agent or credentials. This also applies to a private-registry creation
retry. Explicitly forbidden choices are rejected. If a request omits the
model-source switches, the Portal uses the sources that are available and
stores them as explicit booleans. The default model must belong to an enabled,
authorized source.

The default skills of the Team must satisfy the policy. The runtime applies
its default Gateway budget. The Agent form has no skills or budget editor.

An incomplete Team binding cannot fall back to unrestricted creation. The same
is true for a policy that is absent or in deletion, and for dependencies that
the Portal cannot read. It is also true when no Harness or model source is
available. A Team
without policy or Sandbox settings keeps optional policy assignment.

Policy-controlled native Agents need the AI Gateway. Configuration admission
and runtime enforcement make the final authorization and execution decisions.
The choices in the form do not establish Sandbox readiness.

### Activation boundary

The Team editor manages `agentPolicyRef`, `sandboxRefs`, and
`defaultSandboxRef`. For multiple allowed Sandboxes, the
`sandboxSelection.allowMultiple` of the selected policy is necessary. When you
switch to a restrictive policy, the selections stay visible, and you must
correct them before you save. The Portal does not remove the bindings. The backend reads the policy again before it accepts the Team request,
and admission makes the final decision.

The configuration controls do not establish runtime readiness. See the
[operator notes]({{< repo "app/cetirun-operator/README.md" >}})
for the execution hold, the authority, and the validation boundaries.
