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

Sources: [team event handling]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[team defaults and skills]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[admin team handlers]({{< repo "app/cetirun-portal/internal/handler/admin_team.go" >}}),
and [runtime initialization]({{< repo "app/cetirun-chart/agents/hermes/bootstrap/hermes-init-home.sh" >}}).
